package supabase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/account"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// AccountRepository berbicara ke GoTrue (/auth/v1) dan ke fungsi ensure_profile.
type AccountRepository struct {
	client *sb.Client
}

// NewAccountRepository membuat repository akun.
func NewAccountRepository(client *sb.Client) *AccountRepository {
	return &AccountRepository{client: client}
}

var (
	_ account.Identity = (*AccountRepository)(nil)
	_ account.Profiles = (*AccountRepository)(nil)
)

type gotrueSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type gotrueError struct {
	Code      int    `json:"code"`
	ErrorCode string `json:"error_code"`
	Msg       string `json:"msg"`
	Message   string `json:"message"`
	Desc      string `json:"error_description"`
}

func (e gotrueError) text() string {
	for _, s := range []string{e.Msg, e.Message, e.Desc} {
		if s != "" {
			return s
		}
	}
	return ""
}

// SignUp mendaftarkan akun baru di Supabase Auth.
func (r *AccountRepository) SignUp(ctx context.Context, email, password string) (account.Session, error) {
	return r.callGoTrue(ctx, "/auth/v1/signup", map[string]any{
		"email":    email,
		"password": password,
	}, "")
}

// SignIn menukar email dan kata sandi dengan sesi.
func (r *AccountRepository) SignIn(ctx context.Context, email, password string) (account.Session, error) {
	return r.callGoTrue(ctx, "/auth/v1/token?grant_type=password", map[string]any{
		"email":    email,
		"password": password,
	}, "")
}

// Refresh memperpanjang sesi.
func (r *AccountRepository) Refresh(ctx context.Context, refreshToken string) (account.Session, error) {
	return r.callGoTrue(ctx, "/auth/v1/token?grant_type=refresh_token", map[string]any{
		"refresh_token": refreshToken,
	}, "")
}

// SignOut mencabut sesi di sisi Supabase.
func (r *AccountRepository) SignOut(ctx context.Context, accessToken string) error {
	_, err := r.callGoTrue(ctx, "/auth/v1/logout", map[string]any{}, accessToken)
	return err
}

func (r *AccountRepository) callGoTrue(ctx context.Context, path string, body map[string]any, bearer string) (account.Session, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return account.Session{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.client.BaseURL()+path, strings.NewReader(string(payload)))
	if err != nil {
		return account.Session{}, err
	}

	req.Header.Set("apikey", r.client.AnonKey())
	req.Header.Set("Content-Type", "application/json")

	// Logout memakai token pengguna; sisanya cukup anon key.
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.Header.Set("Authorization", "Bearer "+r.client.AnonKey())
	}

	resp, err := r.client.HTTPClient().Do(req)
	if err != nil {
		return account.Session{}, fmt.Errorf("account: gagal menghubungi Supabase: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return account.Session{}, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return account.Session{}, translateGoTrue(resp.StatusCode, raw)
	}

	// Logout membalas 204 tanpa body.
	if len(raw) == 0 {
		return account.Session{}, nil
	}

	var s gotrueSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return account.Session{}, fmt.Errorf("account: respons Supabase tidak terbaca: %w", err)
	}

	return account.Session{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ExpiresIn:    s.ExpiresIn,
		TokenType:    s.TokenType,
		UserID:       s.User.ID,
		Email:        s.User.Email,
	}, nil
}

// translateGoTrue memetakan kegagalan Supabase ke error domain.
//
// Pesan aslinya sengaja tidak diteruskan ke klien untuk kegagalan kredensial:
// membedakan "email tidak terdaftar" dari "kata sandi salah" memberi tahu
// penyerang akun mana yang benar-benar ada.
func translateGoTrue(status int, raw []byte) error {
	var e gotrueError
	_ = json.Unmarshal(raw, &e)
	text := strings.ToLower(e.text())

	switch {
	case status == http.StatusTooManyRequests,
		strings.Contains(text, "rate limit"),
		strings.Contains(text, "too many"):
		return account.ErrRateLimited

	case strings.Contains(text, "already registered"),
		strings.Contains(text, "already been registered"),
		e.ErrorCode == "user_already_exists":
		return account.ErrEmailTaken

	case strings.Contains(text, "invalid login credentials"),
		strings.Contains(text, "invalid_grant"),
		status == http.StatusBadRequest && strings.Contains(text, "credentials"):
		return account.ErrBadCredentials

	case strings.Contains(text, "refresh token"):
		return account.ErrBadRefresh

	case strings.Contains(text, "password"):
		return account.ErrWeakPassword

	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return account.ErrBadCredentials

	default:
		return fmt.Errorf("account: Supabase membalas %d: %s", status, e.text())
	}
}

type ensureProfileRow struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Created     bool   `json:"created"`
}

// EnsureProfile memanggil fungsi ensure_profile atas nama pengguna baru.
//
// Memakai token yang baru saja diterbitkan, bukan token dari konteks request —
// saat pendaftaran, pemanggilnya belum punya identitas apa pun di konteks.
func (r *AccountRepository) EnsureProfile(
	ctx context.Context,
	accessToken, username, displayName string,
	dob time.Time,
) (string, string, string, bool, error) {
	args := map[string]any{
		"p_username":     nullIfEmpty(username),
		"p_display_name": nullIfEmpty(displayName),
		"p_dob":          nil,
	}
	if !dob.IsZero() {
		args["p_dob"] = dob.Format("2006-01-02")
	}

	var rows []ensureProfileRow
	if err := r.client.RPC(ctx, "ensure_profile", args, &rows, sb.WithToken(accessToken)); err != nil {
		return "", "", "", false, translateAccount(err)
	}
	if len(rows) == 0 {
		return "", "", "", false, errors.New("account: ensure_profile tidak mengembalikan baris")
	}

	row := rows[0]
	return row.ID, row.Username, row.DisplayName, row.Created, nil
}

func translateAccount(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.Code == sqlstateInvalidData {
		return account.ErrInvalidInput
	}
	return err
}
