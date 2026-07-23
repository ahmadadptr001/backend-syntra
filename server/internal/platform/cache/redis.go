// Package cache menyediakan klien Redis.
//
// Redis di Syntra bukan sekadar cache. Ia memegang state yang sengaja TIDAK
// ditaruh di Postgres (lihat docs/erd.md bagian 10): presence, typing
// indicator, unread badge, rate limit counter, dan fanout pesan antar-instance.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedis membuka klien dari URL berformat redis://user:pass@host:port/db
// dan memverifikasinya dengan satu ping.
func NewRedis(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("cache: REDIS_URL tidak valid: %w", err)
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("cache: ping gagal: %w", err)
	}

	return client, nil
}
