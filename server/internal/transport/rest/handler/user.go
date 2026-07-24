package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/user"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// UserService adalah bagian domain user yang dipakai handler REST.
type UserService interface {
	FindByUsername(ctx context.Context, username string) (user.Profile, error)
	Search(ctx context.Context, query string, limit int) ([]user.Profile, error)
	Follow(ctx context.Context, username string) (user.Profile, error)
	Unfollow(ctx context.Context, username string) (user.Profile, error)
	ListFollowing(ctx context.Context) ([]user.Profile, error)
	ListFollowers(ctx context.Context, username string) ([]user.Profile, error)
	FollowRequests(ctx context.Context) ([]user.Profile, error)
	DecideFollowRequest(ctx context.Context, username string, approve bool) error
}

// User menangani endpoint direktori pengguna dan graf pertemanan.
type User struct {
	svc   UserService
	media MediaURLResolver
}

// NewUser membuat handler direktori pengguna.
func NewUser(svc UserService, media MediaURLResolver) *User {
	return &User{svc: svc, media: media}
}

type profileDTO struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	AvatarMediaID string `json:"avatar_media_id,omitempty"`

	FollowerCount  int `json:"follower_count"`
	FollowingCount int `json:"following_count"`

	// "" belum diikuti · "pending" menunggu persetujuan · "accepted" diikuti.
	// Klien memakainya untuk memilih label tombol.
	FollowStatus string `json:"follow_status"`
	IsSelf       bool   `json:"is_self"`

	FollowedAt *time.Time `json:"followed_at,omitempty"`
}

func toProfileDTO(p user.Profile) profileDTO {
	dto := profileDTO{
		ID:             p.ID,
		Username:       p.Username,
		DisplayName:    p.DisplayName,
		AvatarMediaID:  p.AvatarMediaID,
		FollowerCount:  p.FollowerCount,
		FollowingCount: p.FollowingCount,
		FollowStatus:   string(p.FollowStatus),
		IsSelf:         p.IsSelf,
	}
	if !p.FollowedAt.IsZero() {
		followedAt := p.FollowedAt
		dto.FollowedAt = &followedAt
	}
	return dto
}

// GetByUsername menangani GET /api/v1/users/{username}.
//
// Inilah sasaran hasil scan QR: kode berisi username (bukan UUID — lebih
// pendek, lebih mudah dipindai, dan tidak membocorkan struktur internal),
// lalu endpoint ini menukarnya jadi profil yang bisa ditampilkan sebelum
// pengguna memutuskan memulai percakapan.
func (h *User) GetByUsername(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "username tidak boleh kosong")
		return
	}

	profile, err := h.svc.FindByUsername(r.Context(), username)
	if err != nil {
		writeUserError(w, r, err)
		return
	}

	httpx.OK(w, toProfileDTO(profile))
}

// Follow menangani POST /api/v1/users/{username}/follow.
//
// Idempoten. Balasannya berisi profil dengan `follow_status` terbaru, sehingga
// klien bisa langsung memperbarui tombol tanpa memuat ulang profil.
func (h *User) Follow(w http.ResponseWriter, r *http.Request) {
	profile, err := h.svc.Follow(r.Context(), r.PathValue("username"))
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	httpx.OK(w, toProfileDTO(profile))
}

// Unfollow menangani DELETE /api/v1/users/{username}/follow. Idempoten.
func (h *User) Unfollow(w http.ResponseWriter, r *http.Request) {
	profile, err := h.svc.Unfollow(r.Context(), r.PathValue("username"))
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	httpx.OK(w, toProfileDTO(profile))
}

// ListFollowing menangani GET /api/v1/users/me/following.
//
// Selain mengisi layar kontak, ini alat diagnosis pertama saat story seseorang
// tidak muncul di story row: `GET /stories` menyaring berdasarkan daftar ini
// dengan status `accepted`.
func (h *User) ListFollowing(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.svc.ListFollowing(r.Context())
	if err != nil {
		writeUserError(w, r, err)
		return
	}

	items := make([]profileDTO, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, toProfileDTO(p))
	}

	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// Search menangani GET /api/v1/users/search?q=<query>&limit=<n>.
//
// Tanpa q (atau q kosong) mengembalikan saran pengguna — supaya layar
// temukan-orang tidak pernah kosong bagi akun yang belum mengikuti siapa pun.
func (h *User) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}

	profiles, err := h.svc.Search(r.Context(), query, limit)
	if err != nil {
		writeUserError(w, r, err)
		return
	}

	items := make([]profileDTO, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, toProfileDTO(p))
	}

	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// ListMyFollowers menangani GET /api/v1/users/me/followers.
func (h *User) ListMyFollowers(w http.ResponseWriter, r *http.Request) {
	h.writeFollowers(w, r, "")
}

// ListFollowers menangani GET /api/v1/users/{username}/followers.
func (h *User) ListFollowers(w http.ResponseWriter, r *http.Request) {
	h.writeFollowers(w, r, r.PathValue("username"))
}

func (h *User) writeFollowers(w http.ResponseWriter, r *http.Request, username string) {
	profiles, err := h.svc.ListFollowers(r.Context(), username)
	if err != nil {
		writeUserError(w, r, err)
		return
	}

	items := make([]profileDTO, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, toProfileDTO(p))
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// FollowRequests menangani GET /api/v1/users/me/follow-requests.
//
// Hanya relevan untuk akun privat. Tanpa ini, status `pending` menggantung
// selamanya — pemintanya tidak pernah menjadi pengikut, jadi tidak pernah
// bisa melihat story.
func (h *User) FollowRequests(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.svc.FollowRequests(r.Context())
	if err != nil {
		writeUserError(w, r, err)
		return
	}

	items := make([]profileDTO, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, toProfileDTO(p))
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// ApproveFollow menangani POST /api/v1/users/{username}/follow/approve.
func (h *User) ApproveFollow(w http.ResponseWriter, r *http.Request) {
	h.decideFollow(w, r, true)
}

// RejectFollow menangani POST /api/v1/users/{username}/follow/reject.
func (h *User) RejectFollow(w http.ResponseWriter, r *http.Request) {
	h.decideFollow(w, r, false)
}

func (h *User) decideFollow(w http.ResponseWriter, r *http.Request, approve bool) {
	if err := h.svc.DecideFollowRequest(r.Context(), r.PathValue("username"), approve); err != nil {
		writeUserError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, user.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "pengguna tidak ditemukan")

	case errors.Is(err, user.ErrSelfFollow):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "tidak bisa mengikuti diri sendiri")

	case errors.Is(err, user.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "tindakan tidak diizinkan")

	case errors.Is(err, user.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "username tidak valid")

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
