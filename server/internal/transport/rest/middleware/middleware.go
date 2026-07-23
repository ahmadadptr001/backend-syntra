// Package middleware memuat lapisan lintas-endpoint untuk REST.
//
// Urutan pemasangan penting dan tidak sembarangan:
//
//	RequestID -> Recoverer -> Logger -> CORS -> Auth -> handler
//
// RequestID paling luar supaya setiap log punya id. Recoverer di atas Logger
// supaya panic tetap tercatat sebagai 500 yang terukur. Auth paling dalam
// supaya request yang ditolak tetap punya jejak log yang lengkap.
package middleware

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
)

// Middleware membungkus satu handler dengan handler lain.
type Middleware func(http.Handler) http.Handler

// Chain memasang middleware sehingga yang pertama disebut berada paling luar.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

const headerRequestID = "X-Request-ID"

// RequestID memberi setiap request identitas yang bisa dilacak.
// Id dari klien dihormati kalau ada, supaya jejak lintas layanan tersambung.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := r.Header.Get(headerRequestID)
			if rid == "" {
				rid = id.New()
			}

			w.Header().Set(headerRequestID, rid)
			next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), rid)))
		})
	}
}

// Logger mencatat satu baris per request beserta durasi dan status.
func Logger(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				// Probe kesehatan dipanggil terus-menerus; kalau ikut level
				// info, log produksi tidak akan terbaca lagi.
				level = slog.LevelDebug
			}

			log.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", httpx.RequestID(r.Context()),
			)
		})
	}
}

type errKey struct{}

// WithError menyematkan error penyebab ke konteks request.
//
// Dibutuhkan karena handler membalas 500 dengan pesan yang sudah digeneralisasi
// — penyebab aslinya tidak boleh bocor ke klien. Tanpa jalur ini, penyebab itu
// juga tidak sampai ke log, dan yang tersisa hanya "status=500" tanpa petunjuk
// apa pun. Itu persis keadaan yang membuat sebuah bug sulit dilacak.
func WithError(r *http.Request, err error) {
	if err == nil {
		return
	}
	if holder, ok := r.Context().Value(errKey{}).(*errorHolder); ok {
		holder.err = err
	}
}

type errorHolder struct{ err error }

// CaptureErrors menyiapkan wadah error dan mencatatnya kalau ada.
//
// Dipasang di luar Logger supaya error yang tertangkap ikut muncul di baris
// log request yang sama.
func CaptureErrors() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			holder := &errorHolder{}
			ctx := context.WithValue(r.Context(), errKey{}, holder)

			next.ServeHTTP(w, r.WithContext(ctx))

			if holder.err != nil {
				slog.ErrorContext(ctx, "handler error",
					"error", holder.err,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", httpx.RequestID(ctx),
				)
			}
		})
	}
}

// Recoverer mengubah panic menjadi 500 alih-alih menjatuhkan proses.
func Recoverer(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// http.ErrAbortHandler adalah cara net/http memutus
					// respons secara sengaja. Menelannya di sini akan
					// menyamarkan perilaku normal sebagai kesalahan.
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}

					log.Error("http panic",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", httpx.RequestID(r.Context()),
					)
					httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// CORS mengizinkan origin yang terdaftar saja.
//
// Tidak ada wildcard "*": endpoint ini membawa kredensial, dan wildcard
// bersama Access-Control-Allow-Credentials memang dilarang spesifikasi.
func CORS(origins []string) Middleware {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if _, ok := allowed[origin]; ok && origin != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
				h.Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// recorder mengintip status dan jumlah byte respons.
type recorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Hijack meneruskan kemampuan hijack ke ResponseWriter aslinya.
//
// Ini WAJIB ada. Upgrade WebSocket bekerja dengan cara mengambil alih koneksi
// TCP lewat http.Hijacker. Kalau pembungkus di sini tidak meneruskannya,
// endpoint /ws akan gagal upgrade dengan pesan yang menyesatkan — dan
// penyebabnya (sebuah middleware logging) hampir mustahil ditebak.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("middleware: ResponseWriter tidak mendukung hijack")
	}
	return hijacker.Hijack()
}

// Flush meneruskan flush, dibutuhkan streaming response seperti SSE.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
