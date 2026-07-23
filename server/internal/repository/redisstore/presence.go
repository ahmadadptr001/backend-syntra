// Package redisstore menyimpan state efemeral di Redis.
//
// Berbeda dari internal/repository/supabase yang menyimpan kebenaran jangka
// panjang, isi package ini boleh hilang tanpa merusak apa pun — paling buruk
// semua orang tampak offline sebentar sampai klien terhubung ulang.
package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/presence"
)

const (
	keyOnline   = "presence:online:"
	keyLastSeen = "presence:last:"

	// Waktu terakhir terlihat disimpan jauh lebih lama daripada status online,
	// supaya "last seen" tetap bisa ditampilkan berhari-hari kemudian.
	lastSeenTTL = 30 * 24 * time.Hour
)

// Presence memenuhi kontrak presence.Store.
type Presence struct {
	client *redis.Client
}

// NewPresence membuat store presence.
func NewPresence(client *redis.Client) *Presence {
	return &Presence{client: client}
}

var _ presence.Store = (*Presence)(nil)

// SetOnline menandai pengguna online selama ttl.
//
// TTL adalah inti mekanismenya: kalau proses server mati mendadak tanpa sempat
// membersihkan apa pun, kunci ini kedaluwarsa sendiri. Tanpa itu, satu crash
// akan membuat sekumpulan pengguna tampak online selamanya.
func (p *Presence) SetOnline(ctx context.Context, userID string, ttl time.Duration) error {
	if err := p.client.Set(ctx, keyOnline+userID, "1", ttl).Err(); err != nil {
		return fmt.Errorf("presence: gagal menandai online: %w", err)
	}
	return nil
}

// SetOffline menghapus status online dan mencatat waktu terakhir terlihat.
func (p *Presence) SetOffline(ctx context.Context, userID string, at time.Time) error {
	pipe := p.client.TxPipeline()
	pipe.Del(ctx, keyOnline+userID)
	pipe.Set(ctx, keyLastSeen+userID, strconv.FormatInt(at.Unix(), 10), lastSeenTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("presence: gagal menandai offline: %w", err)
	}
	return nil
}

// Query mengambil status sekumpulan pengguna dalam dua perjalanan ke Redis,
// bukan dua per pengguna.
func (p *Presence) Query(ctx context.Context, userIDs []string) (map[string]presence.Status, error) {
	onlineKeys := make([]string, len(userIDs))
	lastKeys := make([]string, len(userIDs))
	for i, uid := range userIDs {
		onlineKeys[i] = keyOnline + uid
		lastKeys[i] = keyLastSeen + uid
	}

	onlineVals, err := p.client.MGet(ctx, onlineKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("presence: gagal membaca status online: %w", err)
	}

	lastVals, err := p.client.MGet(ctx, lastKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("presence: gagal membaca last seen: %w", err)
	}

	out := make(map[string]presence.Status, len(userIDs))
	for i, uid := range userIDs {
		status := presence.Status{
			UserID: uid,
			Online: onlineVals[i] != nil,
		}

		if raw, ok := lastVals[i].(string); ok {
			if unix, convErr := strconv.ParseInt(raw, 10, 64); convErr == nil {
				status.LastSeen = time.Unix(unix, 0).UTC()
			}
		}

		out[uid] = status
	}

	return out, nil
}
