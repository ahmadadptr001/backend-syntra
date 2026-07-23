// Package account membungkus pendaftaran dan login.
//
// Kenapa ini ada, padahal autentikasi ditangani Supabase Auth:
//
// Supabase Auth hanya membuat baris di auth.users. Tabel aplikasi
// (public.users, user_profiles, user_settings) tidak ikut terisi. Akibatnya
// pengguna yang mendaftar langsung lewat SDK Supabase punya akun yang bisa
// login tetapi tidak punya profil — setiap query mengembalikan kosong tanpa
// pesan error yang menjelaskan kenapa. Ini bukan kemungkinan teoretis; sempat
// terjadi 4 baris di auth.users berbanding 3 di public.users.
//
// Package ini menjahit keduanya menjadi satu operasi: daftar ke Supabase, lalu
// segera buat profilnya. Login pun memanggil ensure_profile sebagai jaring
// pengaman untuk akun yang terlanjur yatim.
package account

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

var (
	ErrInvalidInput   = errors.New("account: input tidak valid")
	ErrWeakPassword   = errors.New("account: kata sandi minimal 6 karakter")
	ErrEmailTaken     = errors.New("account: email sudah terdaftar")
	ErrBadCredentials = errors.New("account: email atau kata sandi salah")
	ErrBadRefresh     = errors.New("account: refresh token tidak valid")

	// ErrRateLimited datang dari Supabase, bukan dari batas kita sendiri.
	// Proyek free tier tanpa SMTP kustom hanya mengizinkan beberapa pendaftaran
	// per jam. Dibedakan supaya klien menampilkan "coba lagi nanti", bukan
	// "kesalahan internal" yang membuat pengguna mengira aplikasinya rusak.
	ErrRateLimited = errors.New("account: terlalu banyak percobaan, coba lagi nanti")
)

// Batas yang ditegakkan sebelum permintaan diteruskan ke Supabase.
const (
	MinPasswordLength = 6
	MaxUsernameLength = 32
)

// Session adalah hasil login atau pendaftaran.
type Session struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	TokenType    string

	UserID      string
	Email       string
	Username    string
	DisplayName string
}

// Identity adalah penyedia autentikasi (Supabase GoTrue).
type Identity interface {
	SignUp(ctx context.Context, email, password string) (Session, error)
	SignIn(ctx context.Context, email, password string) (Session, error)
	Refresh(ctx context.Context, refreshToken string) (Session, error)
	SignOut(ctx context.Context, accessToken string) error
}

// Profiles membuat baris profil di tabel aplikasi.
type Profiles interface {
	// EnsureProfile idempoten: kalau profilnya sudah ada, ia hanya membaca.
	EnsureProfile(ctx context.Context, accessToken, username, displayName string, dob time.Time) (id, uname, display string, created bool, err error)
}

// Service memuat alur bisnis pendaftaran dan login.
type Service struct {
	identity Identity
	profiles Profiles
}

// NewService merangkai service.
func NewService(identity Identity, profiles Profiles) *Service {
	return &Service{identity: identity, profiles: profiles}
}

// RegisterInput adalah permintaan pendaftaran.
type RegisterInput struct {
	Email       string
	Password    string
	Username    string
	DisplayName string
	DateOfBirth time.Time
}

// Register mendaftar ke Supabase lalu langsung membuat profilnya.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.Username = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Username), "@"))
	in.DisplayName = strings.TrimSpace(in.DisplayName)

	if !looksLikeEmail(in.Email) {
		return Session{}, ErrInvalidInput
	}
	if len(in.Password) < MinPasswordLength {
		return Session{}, ErrWeakPassword
	}
	if in.Username != "" && !validUsername(in.Username) {
		return Session{}, ErrInvalidInput
	}

	session, err := s.identity.SignUp(ctx, in.Email, in.Password)
	if err != nil {
		return Session{}, err
	}

	// Tanpa access token, profil tidak bisa dibuat sekarang — ini terjadi kalau
	// proyek Supabase mewajibkan konfirmasi email. Akunnya sudah ada dan sah;
	// profilnya akan dibuat saat login pertama lewat jaring pengaman di Login().
	if session.AccessToken == "" {
		return session, nil
	}

	return s.attachProfile(ctx, session, in.Username, in.DisplayName, in.DateOfBirth)
}

// Login menukar email dan kata sandi dengan sesi.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return Session{}, ErrInvalidInput
	}

	session, err := s.identity.SignIn(ctx, email, password)
	if err != nil {
		return Session{}, err
	}

	// Jaring pengaman: memperbaiki akun yang dibuat sebelum endpoint ini ada,
	// atau yang dibuat langsung lewat dashboard Supabase.
	return s.attachProfile(ctx, session, "", "", time.Time{})
}

// Refresh memperpanjang sesi memakai refresh token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Session{}, ErrInvalidInput
	}
	return s.identity.Refresh(ctx, refreshToken)
}

// Logout mencabut sesi di sisi Supabase.
func (s *Service) Logout(ctx context.Context, accessToken string) error {
	if strings.TrimSpace(accessToken) == "" {
		return ErrInvalidInput
	}
	return s.identity.SignOut(ctx, accessToken)
}

// attachProfile melengkapi sesi dengan username dan nama tampil.
//
// Kegagalan di sini tidak membatalkan sesi: pengguna sudah berhasil login, dan
// menolak sesinya karena profil gagal dibaca akan mengunci mereka di luar tanpa
// alasan yang bisa mereka perbaiki.
func (s *Service) attachProfile(ctx context.Context, session Session, username, displayName string, dob time.Time) (Session, error) {
	id, uname, display, _, err := s.profiles.EnsureProfile(ctx, session.AccessToken, username, displayName, dob)
	if err != nil {
		return session, nil
	}

	if id != "" {
		session.UserID = id
	}
	session.Username = uname
	session.DisplayName = display
	return session, nil
}

func looksLikeEmail(v string) bool {
	at := strings.IndexByte(v, '@')
	if at <= 0 || at == len(v)-1 {
		return false
	}
	return strings.IndexByte(v[at+1:], '.') > 0
}

// validUsername hanya mengizinkan huruf, angka, dan underscore — supaya aman
// dipakai di deep link `syntra://u/<username>` dan di kode QR.
func validUsername(v string) bool {
	if len(v) < 3 || len(v) > MaxUsernameLength {
		return false
	}
	for _, r := range v {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}
