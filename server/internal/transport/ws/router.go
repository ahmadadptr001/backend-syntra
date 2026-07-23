package ws

import (
	"context"
	"log/slog"

	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws/protocol"
)

// HandlerFunc menangani satu tipe frame masuk.
type HandlerFunc func(ctx context.Context, c *Client, env protocol.Envelope) error

// ErrorMapper menerjemahkan error (termasuk error domain) menjadi kode dan
// pesan yang aman ditampilkan ke klien.
type ErrorMapper func(err error) (code, message string)

// Router memetakan tipe frame ke handler.
//
// Ini padanan mux HTTP untuk sisi socket: satu tempat untuk melihat seluruh
// perintah yang diterima server, sehingga menambah fitur baru berarti
// menambah satu baris Handle, bukan menambah cabang di switch raksasa.
type Router struct {
	handlers map[string]HandlerFunc
	mapErr   ErrorMapper
	log      *slog.Logger
}

// NewRouter membuat router kosong.
func NewRouter(log *slog.Logger) *Router {
	return &Router{
		handlers: make(map[string]HandlerFunc),
		mapErr:   defaultErrorMapper,
		log:      log,
	}
}

// Handle mendaftarkan handler untuk sebuah tipe frame.
func (r *Router) Handle(frameType string, h HandlerFunc) {
	r.handlers[frameType] = h
}

// SetErrorMapper mengganti penerjemah error bawaan.
func (r *Router) SetErrorMapper(m ErrorMapper) {
	if m != nil {
		r.mapErr = m
	}
}

// Dispatch menjalankan handler yang sesuai dan membalas kegagalan ke klien.
func (r *Router) Dispatch(ctx context.Context, c *Client, env protocol.Envelope) {
	handler, ok := r.handlers[env.Type]
	if !ok {
		c.SendError(env.Ref, protocol.CodeUnknownType, "tipe frame tidak dikenal: "+env.Type)
		return
	}

	// Satu frame cacat tidak boleh menjatuhkan koneksi — apalagi, kalau panic
	// lolos sampai ke readPump, seluruh goroutine koneksi ikut mati dan
	// pengguna terputus tanpa penjelasan.
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("ws: panic saat menangani frame",
				"panic", rec,
				"type", env.Type,
				"client_id", c.ID,
				"user_id", c.UserID,
			)
			c.SendError(env.Ref, protocol.CodeInternal, "terjadi kesalahan internal")
		}
	}()

	if err := handler(ctx, c, env); err != nil {
		code, message := r.mapErr(err)

		// Detail error asli hanya masuk log server; klien menerima pesan
		// yang sudah disaring supaya struktur internal tidak bocor.
		r.log.Warn("ws: handler gagal",
			"error", err,
			"type", env.Type,
			"code", code,
			"client_id", c.ID,
			"user_id", c.UserID,
		)
		c.SendError(env.Ref, code, message)
	}
}

func defaultErrorMapper(error) (string, string) {
	return protocol.CodeInternal, "terjadi kesalahan internal"
}
