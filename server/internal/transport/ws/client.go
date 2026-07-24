package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws/protocol"
)

// Options mengatur perilaku satu koneksi.
type Options struct {
	ReadLimitBytes int64
	WriteWait      time.Duration
	PongWait       time.Duration
	PingInterval   time.Duration
	SendBuffer     int
	MaxTopics      int
}

// Client adalah satu koneksi WebSocket.
//
// Setiap koneksi dijalankan oleh dua goroutine: readPump membaca frame masuk,
// writePump menulis frame keluar. Pemisahan ini wajib karena gorilla/websocket
// hanya mengizinkan SATU penulis pada satu waktu. Semua pengiriman karena itu
// melewati channel send, dan writePump adalah satu-satunya yang menyentuh
// conn.WriteMessage.
type Client struct {
	ID       string
	UserID   string
	DeviceID string

	conn *websocket.Conn
	send chan []byte

	// done ditutup tepat satu kali dan menjadi sinyal berhenti untuk kedua
	// pump. Channel send sengaja TIDAK pernah ditutup: penulisnya banyak
	// (Hub, handler), dan menutup channel yang punya banyak penulis adalah
	// resep panic "send on closed channel".
	done      chan struct{}
	closeOnce sync.Once

	hub    *Hub
	router *Router
	opts   Options
	log    *slog.Logger

	// releasedTopics diisi saat koneksi berakhir, berisi topik yang tadi
	// diikuti. Handler memakainya untuk menyiarkan presence offline ke
	// orang-orang yang memang sedang menyimak topik itu.
	releasedTopics []string

	// TrackPresence menandai apakah status online klien ini boleh dicatat dan
	// disiarkan. Bawaannya true; disetel false untuk pengguna yang mematikan
	// privasi presence, sehingga ia tak pernah tampak online bagi lawan bicara.
	TrackPresence bool

	// OnPong dipanggil tiap pong diterima — dipakai handler untuk menyegarkan
	// TTL presence, supaya koneksi yang hidup tapi sepi tidak "kedaluwarsa jadi
	// offline" padahal pengguna masih tersambung.
	OnPong func()
}

func newClient(conn *websocket.Conn, p auth.Principal, hub *Hub, router *Router, opts Options, log *slog.Logger) *Client {
	clientID := id.New()

	return &Client{
		ID:            clientID,
		UserID:        p.UserID,
		DeviceID:      p.DeviceID,
		conn:          conn,
		send:          make(chan []byte, opts.SendBuffer),
		done:          make(chan struct{}),
		hub:           hub,
		router:        router,
		opts:          opts,
		log:           log.With("client_id", clientID, "user_id", p.UserID),
		TrackPresence: true, // dimatikan oleh handler kalau pengguna menyembunyikan presence
	}
}

// Send mengantre payload untuk dikirim. Mengembalikan false kalau koneksi
// sudah tertutup atau klien terlalu lambat.
//
// Kebijakan backpressure: kalau buffer penuh, koneksi diputus, bukan
// diblokir. Memblokir di sini berarti satu ponsel dengan sinyal buruk bisa
// menahan goroutine milik pengirim — dan lewat Hub, menahan seluruh siaran
// ke topik itu. Klien yang diputus akan reconnect lalu menyinkronkan ulang
// riwayat lewat REST; itu jauh lebih murah daripada menyandera server.
func (c *Client) Send(payload []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}

	select {
	case c.send <- payload:
		return true
	case <-c.done:
		return false
	default:
		c.log.Warn("ws: buffer kirim penuh, koneksi diputus", "buffer", cap(c.send))
		c.Close()
		return false
	}
}

// SendEnvelope mengirim satu frame terstruktur.
func (c *Client) SendEnvelope(env protocol.Envelope) bool {
	payload, err := protocol.Encode(env)
	if err != nil {
		c.log.Error("ws: gagal encode frame", "error", err, "type", env.Type)
		return false
	}
	return c.Send(payload)
}

// SendError mengirim frame kegagalan yang mengacu pada ref tertentu.
func (c *Client) SendError(ref, code, message string) {
	c.SendEnvelope(protocol.NewError(ref, code, message))
}

// Close menghentikan koneksi. Aman dipanggil berkali-kali dan dari goroutine
// mana pun.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

// readPump membaca dan mendispatch frame sampai koneksi berakhir.
func (c *Client) readPump(ctx context.Context) {
	defer func() {
		c.releasedTopics = c.hub.Unregister(c)
		c.Close()
	}()

	c.conn.SetReadLimit(c.opts.ReadLimitBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.opts.PongWait))

	// Setiap pong menggeser deadline baca. Inilah yang membedakan koneksi
	// yang benar-benar mati dari koneksi yang sekadar sedang sepi.
	c.conn.SetPongHandler(func(string) error {
		if c.OnPong != nil {
			c.OnPong()
		}
		return c.conn.SetReadDeadline(time.Now().Add(c.opts.PongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				c.log.Debug("ws: koneksi terputus tidak wajar", "error", err)
			}
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			c.SendError("", protocol.CodeBadRequest, "frame bukan JSON yang valid")
			continue
		}

		c.router.Dispatch(ctx, c, env)
	}
}

// writePump menulis frame keluar dan menjaga heartbeat.
func (c *Client) writePump() {
	ticker := time.NewTicker(c.opts.PingInterval)

	defer func() {
		ticker.Stop()
		// Penutupan conn dipusatkan di sini. readPump yang sedang memblokir
		// di ReadMessage baru akan terbangun setelah conn benar-benar ditutup.
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.opts.WriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				c.log.Debug("ws: gagal menulis frame", "error", err)
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.opts.WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.done:
			// Kirim frame close yang benar supaya klien tahu ini penutupan
			// terencana dan boleh langsung reconnect, bukan menunggu timeout.
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.opts.WriteWait))
			_ = c.conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server closing"),
			)
			return
		}
	}
}
