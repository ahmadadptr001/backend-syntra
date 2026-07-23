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
// format "json" untuk production (mudah di-ingest), "text" untuk lokal.
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	var handler slog.Handler
	if strings.EqualFold(format, "json") {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
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
