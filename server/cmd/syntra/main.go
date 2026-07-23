// Command syntra menjalankan server API + WebSocket Syntra.
//
// main sengaja dibuat setipis mungkin: baca konfigurasi, siapkan logger,
// tangkap sinyal, serahkan ke internal/app. Semua yang bisa diuji berada di
// dalam internal, karena package main tidak bisa diimpor oleh test lain.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ahmadadptr001/backend-syntra/internal/app"
	"github.com/ahmadadptr001/backend-syntra/internal/config"
	"github.com/ahmadadptr001/backend-syntra/internal/platform/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logger belum terbentuk karena levelnya pun berasal dari konfigurasi.
		slog.Error("gagal memuat konfigurasi", "error", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(log)

	// SIGTERM adalah sinyal yang dikirim Kubernetes, Docker, dan sebagian
	// besar PaaS saat deploy. Menangkapnya adalah syarat agar rollout tidak
	// memutus koneksi pengguna di tengah jalan.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("gagal merakit aplikasi", "error", err)
		os.Exit(1)
	}

	if err := application.Run(ctx); err != nil {
		log.Error("aplikasi berhenti dengan error", "error", err)
		os.Exit(1)
	}

	log.Info("shutdown selesai")
}
