package auth

import (
	"context"
	"strings"
)

// DevVerifier menerima token apa pun dan memperlakukannya sebagai id pengguna.
//
// PERINGATAN: ini melumpuhkan autentikasi sepenuhnya. Hanya boleh dipasang
// ketika APP_ENV=development DAN AUTH_DEV_BYPASS=true. config.validate()
// menolak startup kalau kombinasi ini muncul di staging/production, dan
// app.New menulis log peringatan setiap kali verifier ini aktif.
//
// Ada batasan penting yang perlu diketahui: karena token palsu ini bukan JWT
// Supabase, auth.uid() di sisi database akan bernilai NULL dan seluruh policy
// RLS menolak. Artinya DevVerifier hanya berguna untuk menguji jalur socket,
// routing, dan validasi — bukan jalur data. Untuk menguji query sungguhan,
// login-lah lewat Supabase Auth dan pakai JWT aslinya.
type DevVerifier struct{}

// Verify mengembalikan Principal dengan UserID = token.
func (DevVerifier) Verify(_ context.Context, token string) (Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, ErrNoToken
	}

	// Menolak JWT sungguhan, dan ini bukan kehati-hatian berlebihan.
	//
	// Kalau klien mengirim JWT Supabase yang sah sementara bypass menyala,
	// UserID akan berisi seluruh string JWT alih-alih UUID. Akibatnya
	// beruntun dan sulit dilacak: langganan WebSocket menjadi
	// "user:eyJhbGciOi…" lalu ditolak Postgres dengan "invalid input syntax
	// for type uuid", sementara pesan errornya tidak menyebut token sama
	// sekali. Persis itu yang sempat terjadi.
	//
	// Menolaknya di sini mengubah kegagalan senyap menjadi pesan yang
	// langsung menunjuk penyebabnya.
	if looksLikeJWT(token) {
		return Principal{}, ErrDevBypassRejectsJWT
	}

	return Principal{
		UserID:   token,
		DeviceID: "dev-device",
		Scopes:   []string{"chat:read", "chat:write"},
		Token:    token,
	}, nil
}

// looksLikeJWT mengenali tiga segmen base64url yang dipisah titik.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return strings.HasPrefix(token, "eyJ") // header JSON ter-base64
}
