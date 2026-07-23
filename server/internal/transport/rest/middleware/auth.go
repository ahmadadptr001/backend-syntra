package middleware

import (
	"errors"
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
			token, reason := extractToken(r, opts)
			if token == "" {
				httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, reason)
				return
			}

			principal, err := verifier.Verify(r.Context(), token)
			if err != nil {
				// Kesalahan konfigurasi server diteruskan apa adanya: ia
				// menyebut kunci mana yang harus diubah, dan menyembunyikannya
				// di balik pesan generik justru membuat penyebabnya kabur.
				if errors.Is(err, auth.ErrDevBypassRejectsJWT) {
					WithError(r, err)
					httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, err.Error())
					return
				}
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

// extractToken mengambil token dan, kalau gagal, menjelaskan kenapa.
//
// Alasannya dipisahkan karena "tidak ada header" dan "header ada tapi salah
// format" adalah dua masalah yang sangat berbeda di sisi klien — dan pesan
// yang sama untuk keduanya pernah menyita waktu untuk didiagnosis. Pesan ini
// aman ditampilkan: ia menjelaskan bentuk yang diharapkan, bukan membocorkan
// apakah suatu token valid.
func extractToken(r *http.Request, opts AuthOptions) (token, reason string) {
	if header := r.Header.Get(headerAuthorization); header != "" {
		if len(header) > len(bearerPrefix) && strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
			value := strings.TrimSpace(header[len(bearerPrefix):])
			if value == "" {
				return "", `header Authorization berisi "Bearer" tanpa token`
			}
			return value, ""
		}

		// Kasus paling sering: klien mengirim JWT mentah tanpa awalan.
		return "", `format header Authorization salah, harus "Bearer <token>"`
	}

	if opts.AllowDebugHeader {
		if user := r.Header.Get(headerDebugUser); user != "" {
			return user, ""
		}
	}

	if opts.AllowQueryToken {
		if value := strings.TrimSpace(r.URL.Query().Get(queryToken)); value != "" {
			return value, ""
		}
		return "", `token tidak disertakan: kirim header "Authorization: Bearer <token>" atau query ?token=<token>`
	}

	return "", `token tidak disertakan: kirim header "Authorization: Bearer <token>"`
}
