// Package logger membungkus log/slog dari stdlib.
//
// Tidak ada dependensi logging pihak ketiga: slog sudah cukup, dan menjaga
// logger tetap stdlib berarti setiap library yang memakai slog.Default()
// otomatis ikut terformat sama.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New membuat logger sesuai level dan format yang diminta.
//
// Tiga format, masing-masing untuk keperluan berbeda:
//
//	pretty  ringkas dan berwarna — untuk dipantau langsung di terminal
//	text    key=value bawaan slog — lengkap, tapi satu baris sering terlipat
//	json    untuk produksi; dibaca mesin, bukan manusia
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)

	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)

	default: // "pretty" dan nilai tak dikenal
		// Warna hanya dinyalakan kalau keluaran benar-benar ke terminal.
		// Saat diarahkan ke berkas — dan start.ps1 memang melakukannya —
		// kode ANSI akan tampil sebagai sampah seperti "ESC[2m".
		handler = newPrettyHandler(os.Stdout, opts.Level, isTerminal(os.Stdout))
	}

	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// isTerminal menebak apakah keluaran mengarah ke terminal.
//
// Berkas biasa punya mode ModeType nol; pipe, dan character device seperti
// konsol, tidak. Pemeriksaan ini cukup untuk keperluan di sini dan menghindari
// satu dependensi hanya demi mendeteksi TTY.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
