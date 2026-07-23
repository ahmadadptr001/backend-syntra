package middleware

import (
	"net/http"
	"strings"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
)

const (
	headerAuthorization = "Authorization"
	headerDebugUser     = "X-Debug-User"
	queryToken          = "token"
	bearerPrefix        = "Bearer "
)

// AuthOptions mengatur perilaku middleware autentikasi.
type AuthOptions struct {
	// AllowQueryToken mengizinkan token lewat query string.
	//
	// Dibutuhkan karena WebSocket API di browser tidak bisa menyetel header
	// Authorization pada handshake. Konsekuensinya nyata: token ikut tercatat
	// di access log, riwayat browser, dan header Referer.
	//
	// Karena itu nyalakan hanya untuk rute WebSocket, dan begitu klien web
	// benar-benar dibangun, ganti polanya dengan tiket sekali pakai berumur
	// pendek: klien menukar token panjang lewat POST, lalu memakai tiket itu
	// di query string handshake.
	AllowQueryToken bool

	// AllowDebugHeader mengizinkan header X-Debug-User sebagai identitas.
	// Hanya untuk development; config menolak startup kalau ini aktif di
	// staging atau production.
	AllowDebugHeader bool
}

// Auth mewajibkan token yang valid dan menyematkan identitas ke konteks.
func Auth(verifier auth.Verifier, opts AuthOptions) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r, opts)
			if token == "" {
				httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "token tidak disertakan")
				return
			}

			principal, err := verifier.Verify(r.Context(), token)
			if err != nil {
				httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "token tidak valid atau kedaluwarsa")
				return
			}

			// Token mentah ikut dibawa karena setiap panggilan ke Supabase
			// harus meneruskannya agar auth.uid() terisi dan RLS bekerja.
			if principal.Token == "" {
				principal.Token = token
			}

			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func extractToken(r *http.Request, opts AuthOptions) string {
	if header := r.Header.Get(headerAuthorization); header != "" {
		if len(header) > len(bearerPrefix) && strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
			return strings.TrimSpace(header[len(bearerPrefix):])
		}
		return ""
	}

	if opts.AllowDebugHeader {
		if user := r.Header.Get(headerDebugUser); user != "" {
			return user
		}
	}

	if opts.AllowQueryToken {
		return strings.TrimSpace(r.URL.Query().Get(queryToken))
	}

	return ""
}
