package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
)

// Limiter memutuskan apakah sebuah kunci (di sini: id pengguna) boleh lanjut.
// Dipenuhi oleh repository/redisstore.RateLimiter. Kontraknya tanpa error:
// kegagalan infrastruktur ditangani fail-open di dalam implementasinya.
type Limiter interface {
	Allow(ctx context.Context, key string) (bool, time.Duration)
}

// RateLimit membatasi permintaan per pengguna. WAJIB dipasang SETELAH Auth,
// karena ia mengunci pada identitas (auth.UserID), bukan alamat IP — satu IP
// bisa menaungi banyak pengguna (NAT), dan satu pengguna bisa berpindah IP.
//
// Permintaan tanpa identitas (mestinya tidak sampai sini bila Auth mendahului)
// dilewatkan: rate limit adalah lapisan kedua, bukan pengganti autentikasi.
func RateLimit(limiter Limiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := auth.UserID(r.Context())
			if userID == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, retryAfter := limiter.Allow(r.Context(), userID)
			if !allowed {
				// Retry-After dalam detik, dibulatkan ke atas supaya klien tidak
				// mencoba lagi sedetik terlalu awal dan langsung ditolak lagi.
				secs := max(int((retryAfter+time.Second-1)/time.Second), 1)
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				httpx.Fail(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited,
					"terlalu banyak permintaan, coba lagi sebentar")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
