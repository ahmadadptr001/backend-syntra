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
	return Principal{
		UserID:   token,
		DeviceID: "dev-device",
		Scopes:   []string{"chat:read", "chat:write"},
		Token:    token,
	}, nil
}
