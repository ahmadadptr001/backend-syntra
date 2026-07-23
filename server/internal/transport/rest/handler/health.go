// Package handler memuat handler REST.
//
// Handler bertugas menerjemahkan: HTTP masuk menjadi panggilan service, hasil
// service menjadi HTTP keluar. Tidak ada aturan bisnis dan tidak ada SQL.
package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
)

// Check menguji satu dependensi.
type Check func(ctx context.Context) error

// Health menyediakan probe untuk orchestrator.
type Health struct {
	version string
	checks  map[string]Check
}

// NewHealth membuat handler kesehatan.
func NewHealth(version string, checks map[string]Check) *Health {
	return &Health{version: version, checks: checks}
}

// Live menjawab apakah proses hidup.
//
// Sengaja TIDAK menyentuh database. Liveness yang ikut memeriksa dependensi
// adalah kesalahan klasik: saat database sempat terganggu, orchestrator akan
// membunuh semua pod yang sebenarnya sehat dan mengubah gangguan kecil
// menjadi mati total.
func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": h.version,
	})
}

// Ready menjawab apakah instance siap menerima trafik. Di sinilah dependensi
// diperiksa, karena instance tanpa database memang tidak boleh dikirimi request.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	results := make(map[string]string, len(h.checks))
	ready := true

	for name, check := range h.checks {
		if err := check(ctx); err != nil {
			results[name] = "down: " + err.Error()
			ready = false
			continue
		}
		results[name] = "ok"
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}

	httpx.JSON(w, status, map[string]any{
		"ready":        ready,
		"version":      h.version,
		"dependencies": results,
	})
}
