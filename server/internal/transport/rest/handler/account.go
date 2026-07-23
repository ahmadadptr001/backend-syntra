package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/account"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// AccountService adalah bagian domain account yang dipakai handler REST.
type AccountService interface {
	Register(ctx context.Context, in account.RegisterInput) (account.Session, error)
	Login(ctx context.Context, email, password string) (account.Session, error)
	Refresh(ctx context.Context, refreshToken string) (account.Session, error)
	Logout(ctx context.Context, accessToken string) error
}

// Account menangani pendaftaran dan login.
//
// Endpoint ini bersifat publik — ia berada di luar middleware auth, karena
// justru inilah yang menerbitkan tokennya.
type Account struct {
	svc AccountService
}

// NewAccount membuat handler akun.
func NewAccount(svc AccountService) *Account {
	return &Account{svc: svc}
}

type sessionDTO struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`

	UserID      string `json:"user_id,omitempty"`
	Email       string `json:"email,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`

	// PendingConfirmation bernilai true kalau proyek Supabase mewajibkan
	// konfirmasi email: akunnya sudah dibuat tetapi belum ada token, jadi
	// klien harus mengarahkan pengguna ke kotak masuknya.
	PendingConfirmation bool `json:"pending_confirmation,omitempty"`
}

func toSessionDTO(s account.Session) sessionDTO {
	tokenType := s.TokenType
	if tokenType == "" {
		tokenType = "bearer"
	}

	return sessionDTO{
		AccessToken:         s.AccessToken,
		RefreshToken:        s.RefreshToken,
		TokenType:           tokenType,
		ExpiresIn:           s.ExpiresIn,
		UserID:              s.UserID,
		Email:               s.Email,
		Username:            s.Username,
		DisplayName:         s.DisplayName,
		PendingConfirmation: s.AccessToken == "",
	}
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	DateOfBirth string `json:"date_of_birth,omitempty"` // YYYY-MM-DD
}

// Register menangani POST /api/v1/auth/register.
//
// Melakukan dua hal sekaligus: mendaftar ke Supabase Auth, lalu membuat baris
// profil di tabel aplikasi. Kalau klien mendaftar langsung lewat SDK Supabase,
// langkah kedua terlewat dan akunnya jadi tidak punya profil — bisa login,
// tetapi setiap query mengembalikan kosong.
func (h *Account) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	in := account.RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		Username:    req.Username,
		DisplayName: req.DisplayName,
	}

	if req.DateOfBirth != "" {
		dob, err := time.Parse("2006-01-02", req.DateOfBirth)
		if err != nil {
			httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
				"date_of_birth harus berformat YYYY-MM-DD")
			return
		}
		in.DateOfBirth = dob
	}

	session, err := h.svc.Register(r.Context(), in)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}

	httpx.Created(w, toSessionDTO(session))
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login menangani POST /api/v1/auth/login.
func (h *Account) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	session, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}

	httpx.OK(w, toSessionDTO(session))
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh menangani POST /api/v1/auth/refresh.
//
// Access token berumur 1 jam. Klien memanggil endpoint ini saat menerima 401,
// lalu mengulang permintaan yang gagal dengan token baru.
func (h *Account) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	session, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		writeAccountError(w, r, err)
		return
	}

	httpx.OK(w, toSessionDTO(session))
}

// Logout menangani POST /api/v1/auth/logout.
//
// Mengambil token dari header karena yang dicabut adalah sesi pemanggil itu
// sendiri. Balasan tetap 204 walaupun tokennya sudah tidak berlaku — dari
// sudut pandang pengguna, "keluar" tidak boleh bisa gagal.
func (h *Account) Logout(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))

	if token == "" || token == header {
		httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized,
			`kirim header "Authorization: Bearer <token>"`)
		return
	}

	if err := h.svc.Logout(r.Context(), token); err != nil {
		middleware.WithError(r, err)
	}

	httpx.NoContent(w)
}

func writeAccountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, account.ErrEmailTaken):
		httpx.Fail(w, r, http.StatusConflict, httpx.CodeConflict, "email sudah terdaftar")

	case errors.Is(err, account.ErrBadCredentials):
		httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "email atau kata sandi salah")

	case errors.Is(err, account.ErrBadRefresh):
		httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized,
			"refresh token tidak valid, pengguna harus login ulang")

	case errors.Is(err, account.ErrWeakPassword):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "kata sandi minimal 6 karakter")

	case errors.Is(err, account.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "email atau username tidak valid")

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
