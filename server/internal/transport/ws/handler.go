package ws

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws/protocol"
)

// Handler menaikkan koneksi HTTP menjadi WebSocket.
//
// Autentikasi TIDAK dilakukan di sini. Endpoint ini dipasang di belakang
// middleware auth yang sama dengan REST, jadi begitu eksekusi sampai ke sini
// identitas sudah pasti ada di konteks. Satu jalur autentikasi untuk dua
// transport berarti tidak ada kemungkinan salah satunya lebih longgar.
type Handler struct {
	hub      *Hub
	router   *Router
	presence PresenceService
	upgrader websocket.Upgrader
	opts     Options
	log      *slog.Logger
}

// NewHandler membuat handler upgrade.
func NewHandler(hub *Hub, router *Router, presence PresenceService, opts Options, allowedOrigins []string, log *slog.Logger) *Handler {
	return &Handler{
		hub:      hub,
		router:   router,
		presence: presence,
		opts:     opts,
		log:      log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			HandshakeTimeout: 10 * time.Second,
			CheckOrigin:      originChecker(allowedOrigins),
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok || principal.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade sudah menulis respons HTTP-nya sendiri saat gagal.
		h.log.Debug("ws: upgrade gagal", "error", err, "user_id", principal.UserID)
		return
	}

	client := newClient(conn, principal, h.hub, h.router, h.opts, h.log)

	// Pengguna yang mematikan privasi presence tidak dicatat online dan tidak
	// disiarkan — jadi ia tak pernah tampak online bagi lawan bicara. Diperiksa
	// sekali di sini; berlaku sejak koneksi berikutnya setelah setelan diubah.
	if visible, err := h.presence.Visible(r.Context(), principal.UserID); err != nil {
		h.log.Warn("ws: gagal memeriksa visibilitas presence, dianggap terlihat",
			"error", err, "user_id", principal.UserID)
	} else {
		client.TrackPresence = visible
	}

	h.hub.Register(client)

	if client.TrackPresence {
		if err := h.presence.Online(r.Context(), principal.UserID); err != nil {
			// Presence adalah hiasan, bukan syarat. Gagal mencatatnya tidak boleh
			// menghalangi pengguna memakai chat.
			h.log.Warn("ws: gagal menandai online", "error", err, "user_id", principal.UserID)
		}
	}

	go client.writePump()

	h.sendReady(client)

	// readPump sengaja dijalankan di goroutine ini, bukan goroutine baru.
	// Selama ia berjalan, http.Server masih menganggap request aktif, sehingga
	// Shutdown ikut menunggu koneksi socket benar-benar tertutup alih-alih
	// memutusnya begitu saja.
	client.readPump(r.Context())

	// Konteks request sudah selesai begitu readPump kembali, sedangkan dua
	// pekerjaan di bawah masih perlu berjalan. WithoutCancel mempertahankan
	// nilai di dalamnya — termasuk JWT pengguna — tanpa ikut dibatalkan.
	ctx := context.WithoutCancel(r.Context())

	if client.TrackPresence {
		if err := h.presence.Offline(ctx, principal.UserID); err != nil {
			h.log.Warn("ws: gagal menandai offline", "error", err, "user_id", principal.UserID)
		}
		BroadcastPresence(ctx, h.hub, principal.UserID, false, client.releasedTopics)
	}
}

// sendReady memberi tahu klien bahwa koneksi siap, sekaligus mengirim
// parameter heartbeat supaya klien tidak perlu menebak atau menghardcode-nya.
func (h *Handler) sendReady(c *Client) {
	env, err := protocol.NewEvent(protocol.TypeReady, map[string]any{
		"client_id":             c.ID,
		"user_id":               c.UserID,
		"heartbeat_interval_ms": h.opts.PingInterval.Milliseconds(),
		"max_frame_bytes":       h.opts.ReadLimitBytes,
	})
	if err != nil {
		h.log.Error("ws: gagal menyusun frame ready", "error", err)
		return
	}
	c.SendEnvelope(env)
}

// originChecker menentukan origin browser mana yang boleh melakukan handshake.
//
// Ini bukan formalitas. Tanpa pemeriksaan origin, situs mana pun bisa membuka
// koneksi WebSocket ke server ini memakai cookie korban — Cross-Site WebSocket
// Hijacking. Nilai bawaan gorilla memang aman, tapi banyak contoh di internet
// menggantinya dengan "return true"; jangan.
func originChecker(allowed []string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// Klien native (Android/iOS) tidak mengirim Origin sama sekali, dan
		// mereka juga tidak punya cookie ambient — jadi tidak ada yang bisa
		// dibajak. Token tetap wajib lewat middleware auth.
		if origin == "" {
			return true
		}

		for _, a := range allowed {
			if a == origin {
				return true
			}
		}
		return false
	}
}
