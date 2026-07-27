package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/account"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// ProfileService adalah bagian domain account yang dipakai handler profil.
type ProfileService interface {
	Get(ctx context.Context) (account.MyProfile, error)
	Update(ctx context.Context, in account.UpdateProfileInput) error
	ClearCover(ctx context.Context) error
	Block(ctx context.Context, username string) error
	Unblock(ctx context.Context, username string) error
	ListBlocked(ctx context.Context) ([]account.BlockedUser, error)
	ListBlockedBy(ctx context.Context) ([]account.BlockedUser, error)
	RegisterDevice(ctx context.Context, deviceID, platform, pushToken, appVersion string) error
	RevokeDevice(ctx context.Context, deviceID string) error
	Report(ctx context.Context, targetType, targetID, reason, detail string) error
}

// Profile menangani profil sendiri, blokir, perangkat, dan laporan.
type Profile struct {
	svc   ProfileService
	media MediaURLResolver
}

// NewProfile membuat handler profil.
func NewProfile(svc ProfileService, media MediaURLResolver) *Profile {
	return &Profile{svc: svc, media: media}
}

type myProfileDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	Bio         string `json:"bio,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`

	FollowerCount  int `json:"follower_count"`
	FollowingCount int `json:"following_count"`

	IsPrivate       bool   `json:"is_private"`
	DateOfBirth     string `json:"date_of_birth,omitempty"`
	DMPrivacy       string `json:"dm_privacy"`
	StoryPrivacy    string `json:"story_privacy"`
	PresenceVisible bool   `json:"presence_visible"`
	Locale          string `json:"locale"`
}

// GetMe menangani GET /api/v1/users/me.
//
// Berbeda dari GET /users/{username}: di sini ada email dan preferensi privasi
// — hal yang justru tidak boleh ikut terlihat orang lain.
func (h *Profile) GetMe(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context())
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	httpx.OK(w, myProfileDTO{
		ID:              p.ID,
		Username:        p.Username,
		Email:           p.Email,
		DisplayName:     p.DisplayName,
		Bio:             p.Bio,
		AvatarURL:       h.media.PublicURL(p.AvatarKey),
		CoverURL:        h.media.PublicURL(p.CoverKey),
		FollowerCount:   p.FollowerCount,
		FollowingCount:  p.FollowingCount,
		IsPrivate:       p.IsPrivate,
		DateOfBirth:     p.DateOfBirth,
		DMPrivacy:       p.DMPrivacy,
		StoryPrivacy:    p.StoryPrivacy,
		PresenceVisible: p.PresenceVisible,
		Locale:          p.Locale,
	})
}

type updateProfileRequest struct {
	DisplayName     *string `json:"display_name,omitempty"`
	Bio             *string `json:"bio,omitempty"`
	AvatarMediaID   *string `json:"avatar_media_id,omitempty"`
	CoverMediaID    *string `json:"cover_media_id,omitempty"`
	Username        *string `json:"username,omitempty"`
	IsPrivate       *bool   `json:"is_private,omitempty"`
	DMPrivacy       *string `json:"dm_privacy,omitempty"`
	StoryPrivacy    *string `json:"story_privacy,omitempty"`
	PresenceVisible *bool   `json:"presence_visible,omitempty"`
}

// UpdateMe menangani PATCH /api/v1/users/me.
//
// Field yang tidak dikirim (nil) tidak diubah, jadi klien cukup mengirim yang
// benar-benar berubah.
func (h *Profile) UpdateMe(w http.ResponseWriter, r *http.Request) {
	var req updateProfileRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	err := h.svc.Update(r.Context(), account.UpdateProfileInput{
		DisplayName:     req.DisplayName,
		Bio:             req.Bio,
		AvatarMediaID:   req.AvatarMediaID,
		CoverMediaID:    req.CoverMediaID,
		Username:        req.Username,
		IsPrivate:       req.IsPrivate,
		DMPrivacy:       req.DMPrivacy,
		StoryPrivacy:    req.StoryPrivacy,
		PresenceVisible: req.PresenceVisible,
	})
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	// Balas profil terbaru supaya klien tidak perlu memuat ulang.
	h.GetMe(w, r)
}

// ClearCover menangani DELETE /api/v1/users/me/cover — hapus background profil.
func (h *Profile) ClearCover(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ClearCover(r.Context()); err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type blockedDTO struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// Block menangani POST /api/v1/users/{username}/block.
func (h *Profile) Block(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Block(r.Context(), r.PathValue("username")); err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Unblock menangani DELETE /api/v1/users/{username}/block.
func (h *Profile) Unblock(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Unblock(r.Context(), r.PathValue("username")); err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ListBlockedBy menangani GET /api/v1/users/me/blocked-by.
func (h *Profile) ListBlockedBy(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListBlockedBy(r.Context())
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	items := make([]blockedDTO, 0, len(list))
	for _, b := range list {
		items = append(items, blockedDTO{UserID: b.UserID, Username: b.Username})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// ListBlocked menangani GET /api/v1/users/me/blocked.
func (h *Profile) ListBlocked(w http.ResponseWriter, r *http.Request) {
	blocked, err := h.svc.ListBlocked(r.Context())
	if err != nil {
		writeProfileError(w, r, err)
		return
	}

	items := make([]blockedDTO, 0, len(blocked))
	for _, b := range blocked {
		items = append(items, blockedDTO{
			UserID:      b.UserID,
			Username:    b.Username,
			DisplayName: b.DisplayName,
			AvatarURL:   h.media.PublicURL(b.AvatarKey),
		})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

type registerDeviceRequest struct {
	DeviceID   string `json:"device_id"`
	Platform   string `json:"platform"`
	PushToken  string `json:"push_token"`
	AppVersion string `json:"app_version,omitempty"`
}

// RegisterDevice menangani POST /api/v1/devices — untuk push notification.
func (h *Profile) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req registerDeviceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	err := h.svc.RegisterDevice(r.Context(), req.DeviceID, req.Platform, req.PushToken, req.AppVersion)
	if err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// RevokeDevice menangani DELETE /api/v1/devices/{id}.
func (h *Profile) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeDevice(r.Context(), r.PathValue("id")); err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type reportRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
}

// Report menangani POST /api/v1/reports.
func (h *Profile) Report(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	err := h.svc.Report(r.Context(), req.TargetType, req.TargetID, req.Reason, strings.TrimSpace(req.Detail))
	if err != nil {
		writeProfileError(w, r, err)
		return
	}
	httpx.Created(w, map[string]string{"status": "received"})
}

func writeProfileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, account.ErrProfileNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "tidak ditemukan")

	case errors.Is(err, account.ErrCannotBlockSelf):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "tidak bisa memblokir diri sendiri")

	case errors.Is(err, account.ErrUsernameTaken):
		httpx.Fail(w, r, http.StatusConflict, httpx.CodeConflict, "username sudah dipakai")

	case errors.Is(err, account.ErrInvalidUsername):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
			"username harus 3–30 karakter, diawali huruf, hanya huruf kecil/angka/titik/garis bawah")

	case errors.Is(err, account.ErrBadPrivacy),
		errors.Is(err, account.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "input tidak valid")

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
