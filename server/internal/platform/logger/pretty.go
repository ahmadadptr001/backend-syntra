package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
)

// prettyHandler menulis log yang enak dibaca manusia saat dipantau langsung.
//
// TextHandler bawaan slog dirancang untuk diparse mesin: setiap field ditulis
// sebagai key=value lengkap dengan timestamp berzona waktu, sehingga satu baris
// request sepele pun melebihi lebar terminal dan terlipat. Handler ini menukar
// kelengkapan itu dengan keterbacaan:
//
//	20:24:41 INF  http request           GET /readyz 200 76ms
//	20:24:37 WRN  LiveKit belum dikonfigurasi
//
// Hanya untuk pengembangan. Di produksi pakai LOG_FORMAT=json — log di sana
// dibaca mesin, dan warna ANSI justru mengotori sistem pengumpul log.
type prettyHandler struct {
	mu    *sync.Mutex
	out   io.Writer
	level slog.Leveler
	color bool

	// attrs dan groups menampung hasil WithAttrs/WithGroup. slog boleh
	// memanggil keduanya berkali-kali, dan tiap hasilnya harus berdiri sendiri.
	attrs  []slog.Attr
	groups []string
}

// Kode warna ANSI. Sengaja hanya empat, supaya keluaran tetap tenang.
const (
	ansiReset = "\033[0m"
	ansiDim   = "\033[2m"
	ansiRed   = "\033[31m"
	ansiAmber = "\033[33m"
	ansiCyan  = "\033[36m"
)

// newPrettyHandler membuat handler ringkas.
func newPrettyHandler(w io.Writer, level slog.Leveler, color bool) *prettyHandler {
	return &prettyHandler{
		mu:    &sync.Mutex{},
		out:   w,
		level: level,
		color: color,
	}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	// Waktu saja tanpa tanggal: saat memantau langsung, tanggalnya sudah jelas
	// dan hanya memakan lebar.
	b.WriteString(h.paint(ansiDim, r.Time.Format("15:04:05")))
	b.WriteByte(' ')
	b.WriteString(h.levelLabel(r.Level))
	b.WriteByte(' ')

	// Pesan dibuat rata kolom supaya field di kanannya sejajar dan mudah
	// dipindai secara vertikal.
	msg := r.Message
	b.WriteString(msg)
	if pad := 24 - len(msg); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}

	// Field request HTTP diangkat ke depan dalam bentuk ringkas, karena
	// itulah yang paling sering dicari saat memantau.
	var (
		method, pathVal, reqID string
		status, durMs          int64
		rest                   []slog.Attr
	)

	collect := func(a slog.Attr) bool {
		switch a.Key {
		case "method":
			method = a.Value.String()
		case "path":
			pathVal = a.Value.String()
		case "status":
			status = a.Value.Int64()
		case "duration_ms":
			durMs = a.Value.Int64()
		case "request_id":
			reqID = a.Value.String()
		default:
			rest = append(rest, a)
		}
		return true
	}

	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(collect)

	if method != "" {
		b.WriteString(fmt.Sprintf("%-6s %s", method, pathVal))
		if status != 0 {
			b.WriteByte(' ')
			b.WriteString(h.paint(statusColor(status), fmt.Sprintf("%d", status)))
		}
		if durMs > 0 {
			b.WriteString(h.paint(ansiDim, fmt.Sprintf(" %dms", durMs)))
		}
	}

	// Sisanya diurutkan supaya posisi field tidak berpindah-pindah antar baris.
	sort.Slice(rest, func(i, j int) bool { return rest[i].Key < rest[j].Key })
	for _, a := range rest {
		b.WriteByte(' ')
		b.WriteString(h.paint(ansiDim, h.qualify(a.Key)+"="))
		b.WriteString(shorten(a.Value.String()))
	}

	// Request id ditaruh paling belakang dan diringkas: yang dibutuhkan saat
	// membaca sekilas hanya potongan awal untuk mencocokkan dua baris. Nilai
	// lengkapnya tetap ada di log json maupun di header respons.
	if reqID != "" {
		b.WriteString(h.paint(ansiDim, " #"+shortID(reqID)))
	}

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *prettyHandler) levelLabel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return h.paint(ansiRed, "ERR")
	case l >= slog.LevelWarn:
		return h.paint(ansiAmber, "WRN")
	case l >= slog.LevelInfo:
		return h.paint(ansiCyan, "INF")
	default:
		return h.paint(ansiDim, "DBG")
	}
}

func (h *prettyHandler) qualify(key string) string {
	if len(h.groups) == 0 {
		return key
	}
	return path.Join(append(append([]string{}, h.groups...), key)...)
}

func (h *prettyHandler) paint(code, s string) string {
	if !h.color {
		return s
	}
	return code + s + ansiReset
}

func statusColor(status int64) string {
	switch {
	case status >= 500:
		return ansiRed
	case status >= 400:
		return ansiAmber
	default:
		return ansiDim
	}
}

// shorten memotong nilai yang terlalu panjang. Tujuannya menjaga satu kejadian
// tetap satu baris — begitu baris terlipat, keunggulan format ini hilang.
func shorten(v string) string {
	const max = 60
	if len(v) <= max {
		return v
	}
	return v[:max-1] + "…"
}

// shortID mengambil segmen pertama UUID, cukup untuk mencocokkan baris secara
// visual tanpa memakan lebar.
func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
