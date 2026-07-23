// Package ws adalah lapisan transport WebSocket.
//
// Pembagian tugas di dalam package ini:
//
//	hub.go        registry koneksi + routing berbasis topik
//	client.go     satu koneksi: read pump, write pump, heartbeat
//	router.go     dispatch frame masuk ke handler
//	handler.go    upgrade HTTP -> WebSocket dan handler per tipe frame
//	publisher.go  adaptor yang membuat Hub memenuhi port Publisher milik domain
package ws

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

// Bridge menyiarkan payload antar-instance server.
// Diimplementasikan oleh internal/platform/pubsub.
type Bridge interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Subscribe(ctx context.Context, handler func(topic string, payload []byte)) error
}

// Hub adalah registry koneksi aktif pada satu proses.
//
// Batasannya penting untuk dipahami: Hub HANYA mengenal klien di proses ini.
// Pengiriman lintas instance seluruhnya bergantung pada Bridge. Karena itu
// gunakan Publish untuk apa pun yang harus sampai ke semua pengguna, dan
// simpan PublishLocal untuk kasus yang memang khusus instance ini.
type Hub struct {
	mu sync.RWMutex

	// clients memetakan koneksi ke daftar topik yang ia ikuti. Kepemilikan
	// daftar topik ditaruh di Hub, bukan di Client, supaya seluruh mutasi
	// registry berada di bawah satu mutex — tidak ada urutan lock yang perlu
	// dijaga, jadi tidak ada peluang deadlock.
	clients map[*Client]map[string]struct{}

	byUser map[string]map[*Client]struct{}
	topics map[string]map[*Client]struct{}

	bridge Bridge
	log    *slog.Logger
}

// Stats adalah ringkasan kondisi hub, dipakai endpoint metrik dan debug.
type Stats struct {
	Connections int `json:"connections"`
	Users       int `json:"users"`
	Topics      int `json:"topics"`
}

// NewHub membuat hub kosong. bridge boleh nil untuk mode instance tunggal.
func NewHub(bridge Bridge, log *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[*Client]map[string]struct{}),
		byUser:  make(map[string]map[*Client]struct{}),
		topics:  make(map[string]map[*Client]struct{}),
		bridge:  bridge,
		log:     log,
	}
}

// Run menyalurkan siaran dari instance lain ke klien lokal.
// Memblokir sampai ctx dibatalkan.
func (h *Hub) Run(ctx context.Context) error {
	if h.bridge == nil {
		h.log.Warn("ws: berjalan tanpa bridge, siaran tidak akan sampai ke instance lain")
		<-ctx.Done()
		return nil
	}

	return h.bridge.Subscribe(ctx, func(t string, payload []byte) {
		h.PublishLocal(t, payload)
	})
}

// Register memasukkan klien ke registry dan langsung melanggankannya ke
// topik pribadinya, supaya notifikasi bisa sampai tanpa klien perlu subscribe.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c] = make(map[string]struct{})
	if h.byUser[c.UserID] == nil {
		h.byUser[c.UserID] = make(map[*Client]struct{})
	}
	h.byUser[c.UserID][c] = struct{}{}
	h.mu.Unlock()

	h.Subscribe(c, topic.User(c.UserID))

	h.log.Debug("ws: klien terhubung", "client_id", c.ID, "user_id", c.UserID)
}

// Unregister mengeluarkan klien beserta seluruh langganannya.
//
// Mengembalikan daftar topik yang tadi diikuti klien. Pemanggil
// membutuhkannya untuk menyiarkan kabar terakhir — misalnya presence menjadi
// offline — ke orang-orang yang memang sedang menyimak topik tersebut.
// Setelah fungsi ini selesai, informasi itu sudah tidak bisa didapat lagi.
func (h *Hub) Unregister(c *Client) []string {
	h.mu.Lock()
	released := make([]string, 0, len(h.clients[c]))
	for t := range h.clients[c] {
		released = append(released, t)
		if set, ok := h.topics[t]; ok {
			delete(set, c)
			if len(set) == 0 {
				delete(h.topics, t)
			}
		}
	}
	delete(h.clients, c)

	if set, ok := h.byUser[c.UserID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byUser, c.UserID)
		}
	}
	h.mu.Unlock()

	h.log.Debug("ws: klien terputus", "client_id", c.ID, "user_id", c.UserID)
	return released
}

// Subscribe melanggankan klien ke satu atau lebih topik.
func (h *Hub) Subscribe(c *Client, names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	owned, ok := h.clients[c]
	if !ok {
		return // klien sudah terlepas
	}

	for _, name := range names {
		if name == "" {
			continue
		}
		owned[name] = struct{}{}
		if h.topics[name] == nil {
			h.topics[name] = make(map[*Client]struct{})
		}
		h.topics[name][c] = struct{}{}
	}
}

// Unsubscribe melepas langganan klien dari topik tertentu.
func (h *Hub) Unsubscribe(c *Client, names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	owned, ok := h.clients[c]
	if !ok {
		return
	}

	for _, name := range names {
		delete(owned, name)
		if set, ok := h.topics[name]; ok {
			delete(set, c)
			if len(set) == 0 {
				delete(h.topics, name)
			}
		}
	}
}

// PublishLocal mengirim payload ke pelanggan topik di instance ini saja.
// Mengembalikan jumlah klien yang menerima.
func (h *Hub) PublishLocal(name string, payload []byte) int {
	h.mu.RLock()
	subscribers := make([]*Client, 0, len(h.topics[name]))
	for c := range h.topics[name] {
		subscribers = append(subscribers, c)
	}
	h.mu.RUnlock()

	// Pengiriman dilakukan setelah lock dilepas. Client.Send bisa memutus
	// koneksi yang lambat, dan itu tidak boleh terjadi sambil memegang lock
	// registry.
	delivered := 0
	for _, c := range subscribers {
		if c.Send(payload) {
			delivered++
		}
	}
	return delivered
}

// Publish menyiarkan ke instance ini sekaligus ke seluruh instance lain.
func (h *Hub) Publish(ctx context.Context, name string, payload []byte) error {
	h.PublishLocal(name, payload)

	if h.bridge == nil {
		return nil
	}
	return h.bridge.Publish(ctx, name, payload)
}

// SendToUser mengirim ke seluruh perangkat milik satu pengguna di instance ini.
//
// Untuk pengiriman yang harus sampai lintas instance, pakai
// Publish(ctx, topic.User(userID), payload).
func (h *Hub) SendToUser(userID string, payload []byte) int {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.byUser[userID]))
	for c := range h.byUser[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	delivered := 0
	for _, c := range targets {
		if c.Send(payload) {
			delivered++
		}
	}
	return delivered
}

// TopicCount mengembalikan jumlah langganan aktif sebuah klien.
func (h *Hub) TopicCount(c *Client) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[c])
}

// IsSubscribed memeriksa apakah klien sedang berlangganan sebuah topik.
//
// Dipakai sebagai otorisasi murah untuk aksi efemeral seperti indikator
// mengetik: langganan hanya bisa didapat setelah lolos pemeriksaan
// keanggotaan, jadi keberadaannya sudah membuktikan hak akses.
func (h *Hub) IsSubscribed(c *Client, name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	owned, ok := h.clients[c]
	if !ok {
		return false
	}
	_, subscribed := owned[name]
	return subscribed
}

// Stats mengembalikan ringkasan registry.
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return Stats{
		Connections: len(h.clients),
		Users:       len(h.byUser),
		Topics:      len(h.topics),
	}
}

// CloseAll menutup seluruh koneksi. Dipanggil saat shutdown supaya klien
// menerima frame close yang benar dan bisa langsung reconnect ke instance
// lain, alih-alih menunggu timeout.
func (h *Hub) CloseAll() {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.Close()
	}
}
