// Package httpx menyeragamkan bentuk request dan response REST.
//
// Tujuannya satu: setiap endpoint membalas dengan bentuk yang sama, sehingga
// klien Kotlin cukup punya satu tipe pembungkus dan satu penanganan error.
//
//	{"data": {...}}                      sukses
//	{"data": [...], "meta": {...}}       sukses berpaginasi
//	{"error": {"code": "...", ...}}      gagal
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Kode error. Sengaja dibuat sama persis dengan kode di protokol WebSocket
// supaya klien bisa memakai satu penerjemah error untuk kedua transport.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeTooLarge     = "payload_too_large"
	CodeRateLimited  = "rate_limited"
	CodeInternal     = "internal"
)

// MaxBodyBytes membatasi ukuran body JSON. Upload media TIDAK lewat sini —
// media memakai presigned URL langsung ke object storage.
const MaxBodyBytes = 1 << 20 // 1 MB

// Response adalah pembungkus seluruh respons REST.
type Response struct {
	Data  any    `json:"data,omitempty"`
	Meta  any    `json:"meta,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Error adalah detail kegagalan.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// JSON menulis respons berformat JSON.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Header sudah terkirim, jadi tidak ada lagi yang bisa dilakukan
		// selain mencatatnya. Koneksi akan ditutup oleh net/http.
		_ = err
	}
}

// OK membalas 200 dengan data.
func OK(w http.ResponseWriter, data any) {
	JSON(w, http.StatusOK, Response{Data: data})
}

// Page membalas 200 dengan data dan metadata pagination.
func Page(w http.ResponseWriter, data, meta any) {
	JSON(w, http.StatusOK, Response{Data: data, Meta: meta})
}

// Created membalas 201.
func Created(w http.ResponseWriter, data any) {
	JSON(w, http.StatusCreated, Response{Data: data})
}

// NoContent membalas 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Fail membalas kegagalan, lengkap dengan request id supaya laporan pengguna
// bisa langsung dicocokkan dengan baris log.
func Fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	JSON(w, status, Response{
		Error: &Error{
			Code:      code,
			Message:   message,
			RequestID: RequestID(r.Context()),
		},
	})
}

// DecodeJSON membaca body JSON dengan batas ukuran dan penolakan field asing.
//
// DisallowUnknownFields dinyalakan dengan sengaja: kalau klien mengirim
// "conversationId" padahal server menunggu "conversation_id", lebih baik
// gagal terang-terangan daripada diam-diam memproses percakapan kosong.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("body melebihi %d byte: %w", MaxBodyBytes, err)
		}
		return fmt.Errorf("body bukan JSON yang valid: %w", err)
	}

	// Tolak body yang berisi lebih dari satu dokumen JSON.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body hanya boleh berisi satu objek JSON")
	}

	return nil
}

type requestIDKey struct{}

// WithRequestID menyematkan id request ke konteks.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID mengambil id request dari konteks. Mengembalikan string kosong
// kalau middleware request id belum terpasang.
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}
