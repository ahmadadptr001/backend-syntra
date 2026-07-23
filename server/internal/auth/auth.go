// Package auth memegang identitas pemanggil.
//
// Package ini berdiri sendiri, di luar transport, karena REST dan WebSocket
// sama-sama membutuhkannya. Kalau helper konteks ini diletakkan di dalam
// package rest, maka ws harus mengimpor rest — dua transport jadi saling
// terikat tanpa alasan.
package auth

import (
	"context"
	"errors"
)

// Kesalahan yang dikenali lapisan transport.
var (
	ErrNoToken      = errors.New("auth: token tidak disertakan")
	ErrInvalidToken = errors.New("auth: token tidak valid atau kedaluwarsa")

	// ErrDevBypassRejectsJWT muncul ketika klien mengirim JWT sungguhan
	// sementara AUTH_DEV_BYPASS masih menyala. Kombinasi itu tidak pernah
	// benar: bypass memperlakukan token sebagai id pengguna apa adanya,
	// sehingga JWT menghasilkan UserID yang rusak.
	ErrDevBypassRejectsJWT = errors.New(
		"auth: AUTH_DEV_BYPASS masih true padahal klien mengirim JWT Supabase — setel AUTH_DEV_BYPASS=false di .env")
)

// Principal adalah identitas yang sudah terverifikasi.
type Principal struct {
	UserID   string
	DeviceID string
	Scopes   []string

	// Token adalah JWT mentah milik pengguna.
	//
	// Disimpan karena setiap panggilan ke Supabase harus meneruskannya:
	// tanpa token ini auth.uid() di sisi database bernilai NULL dan seluruh
	// policy RLS menolak. Jadi ini bukan kebocoran abstraksi yang malas —
	// token memang bagian dari identitas pemanggil pada arsitektur ini.
	//
	// Jangan pernah menuliskannya ke log.
	Token string
}

// HasScope memeriksa kepemilikan sebuah scope.
func (p Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Verifier menukar token mentah menjadi Principal.
//
// Dibuat sebagai interface supaya strategi token (JWT RS256, sesi opaque di
// Redis, atau bypass development) bisa ditukar tanpa menyentuh middleware.
type Verifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

type ctxKey struct{}

// WithPrincipal menyematkan identitas ke konteks request.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext mengambil identitas dari konteks.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// UserID adalah jalan pintas ketika hanya id pengguna yang dibutuhkan.
func UserID(ctx context.Context) string {
	p, _ := FromContext(ctx)
	return p.UserID
}
