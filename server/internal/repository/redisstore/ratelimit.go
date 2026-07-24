package redisstore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter membatasi permintaan per kunci memakai jendela tetap di Redis.
//
// Jendela tetap dipilih ketimbang sliding window karena cukup untuk tujuannya
// (mencegah satu pengguna membanjiri server) dan hanya butuh satu INCR — murah,
// dan tidak menyimpan riwayat timestamp per permintaan. Konsekuensinya: di batas
// jendela, seorang pengguna bisa mengirim hingga ~2× limit dalam ledakan singkat.
// Untuk pelindung anti-banjir per-pengguna, itu masih dapat diterima; kalau kelak
// butuh presisi, ganti ke sliding window atau leaky bucket.
type RateLimiter struct {
	client *redis.Client
	limit  int
	window time.Duration
	log    *slog.Logger
}

// NewRateLimiter membuat limiter dengan batas per jendela.
//
// limit <= 0 mematikan pembatasan (lihat Disabled) — sengaja, supaya
// RATE_LIMIT_PER_MINUTE=0 di konfigurasi benar-benar mematikannya, bukan malah
// memblokir semua orang.
func NewRateLimiter(client *redis.Client, limit int, window time.Duration, log *slog.Logger) *RateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{client: client, limit: limit, window: window, log: log}
}

// Disabled melaporkan apakah limiter tidak membatasi apa pun: limit non-positif
// atau tanpa client Redis. Middleware bisa memakainya untuk melewati pembungkusan.
func (r *RateLimiter) Disabled() bool {
	return r == nil || r.client == nil || r.limit <= 0
}

// Allow menaikkan penghitung kunci pada jendela berjalan dan memutuskan apakah
// permintaan boleh lanjut. Saat ditolak, mengembalikan sisa waktu sampai jendela
// reset (untuk header Retry-After).
//
// Kontraknya sengaja tanpa error: gagal menghubungi Redis membuat permintaan
// DILOLOSKAN (fail-open) dan hanya dicatat di log. Rate limit adalah pelindung,
// bukan gerbang — memutus semua orang gara-gara Redis tersendat jauh lebih buruk
// daripada sesekali melewatkan batas. Menyembunyikan keputusan fail-open di sini
// (bukan mengembalikan (true, err) yang ambigu) mencegah pemanggil tanpa sengaja
// menutup gerbang saat memeriksa err.
func (r *RateLimiter) Allow(ctx context.Context, key string) (bool, time.Duration) {
	if r.Disabled() {
		return true, 0
	}

	now := time.Now()
	windowStart := now.Truncate(r.window)
	redisKey := fmt.Sprintf("rl:%s:%d", key, windowStart.Unix())

	// INCR dan pemasangan TTL dijalankan dalam satu pipeline: satu perjalanan
	// bolak-balik, dan TTL dijamin ikut terpasang begitu penghitung dibuat —
	// jadi kunci tidak pernah tertinggal tanpa kedaluwarsa (kebocoran memori).
	// ExpireNX hanya memasang TTL bila belum ada, sehingga jendela tak pernah
	// diperpanjang permintaan berikutnya, tanpa perlu cek count==1 yang rawan race.
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, redisKey)
	pipe.ExpireNX(ctx, redisKey, r.window)
	if _, err := pipe.Exec(ctx); err != nil {
		if r.log != nil {
			r.log.Warn("ratelimit: Redis gagal, permintaan diloloskan (fail-open)",
				"error", err, "key", key)
		}
		return true, 0
	}

	if incr.Val() > int64(r.limit) {
		return false, time.Until(windowStart.Add(r.window))
	}
	return true, 0
}
