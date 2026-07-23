// Package pubsub menyiarkan event WebSocket antar-instance server.
//
// Kenapa ini ada: satu Hub hanya mengenal klien yang terhubung ke proses itu
// sendiri. Begitu server dijalankan lebih dari satu replica — dan itu pasti
// terjadi — pesan dari klien di instance A tidak akan pernah sampai ke klien
// di instance B tanpa lapisan ini.
//
// Implementasi memakai Redis Pub/Sub. Perlu dicatat: Redis Pub/Sub bersifat
// at-most-once dan tidak persisten. Kalau ada instance yang sedang restart,
// event yang lewat saat itu hilang. Untuk chat, klien menutup lubang ini
// dengan sinkronisasi ulang lewat REST (GET pesan sejak cursor terakhir)
// setiap kali socket terhubung kembali — bukan dengan mengandalkan pub/sub.
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
)

const defaultPrefix = "syntra:ws:"

// Redis mengimplementasikan kontrak Bridge di package transport/ws.
type Redis struct {
	client     *redis.Client
	prefix     string
	instanceID string
	log        *slog.Logger
}

// envelope membungkus payload agar instance pengirim bisa mengabaikan
// gemanya sendiri. Tanpa ini, pengirim akan mengirim frame dua kali ke
// kliennya: sekali lokal, sekali dari pub/sub.
type envelope struct {
	Origin  string          `json:"origin"`
	Payload json.RawMessage `json:"payload"`
}

// NewRedis membuat bridge. instanceID harus unik per proses.
func NewRedis(client *redis.Client, instanceID string, log *slog.Logger) *Redis {
	return &Redis{
		client:     client,
		prefix:     defaultPrefix,
		instanceID: instanceID,
		log:        log,
	}
}

// Publish menyiarkan payload ke seluruh instance lain.
func (r *Redis) Publish(ctx context.Context, topic string, payload []byte) error {
	body, err := json.Marshal(envelope{
		Origin:  r.instanceID,
		Payload: json.RawMessage(payload),
	})
	if err != nil {
		return fmt.Errorf("pubsub: gagal membungkus payload: %w", err)
	}

	if err := r.client.Publish(ctx, r.prefix+topic, body).Err(); err != nil {
		return fmt.Errorf("pubsub: gagal publish ke topik %q: %w", topic, err)
	}
	return nil
}

// Subscribe mendengarkan seluruh topik dan memanggil handler untuk setiap
// event yang berasal dari instance lain. Fungsi ini memblokir sampai ctx
// dibatalkan, jadi jalankan di goroutine tersendiri.
func (r *Redis) Subscribe(ctx context.Context, handler func(topic string, payload []byte)) error {
	sub := r.client.PSubscribe(ctx, r.prefix+"*")
	defer func() {
		if err := sub.Close(); err != nil {
			r.log.Warn("pubsub: gagal menutup subscription", "error", err)
		}
	}()

	ch := sub.Channel()

	for {
		select {
		case <-ctx.Done():
			return nil

		case msg, ok := <-ch:
			if !ok {
				return errors.New("pubsub: kanal subscription tertutup")
			}

			var env envelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				r.log.Warn("pubsub: payload tidak bisa dibaca", "channel", msg.Channel, "error", err)
				continue
			}

			// Abaikan siaran dari diri sendiri; Hub sudah mengirimnya secara lokal.
			if env.Origin == r.instanceID {
				continue
			}

			handler(strings.TrimPrefix(msg.Channel, r.prefix), env.Payload)
		}
	}
}
