package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/live"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// LiveService adalah bagian domain live yang dipakai handler REST.
type LiveService interface {
	Create(ctx context.Context, in live.CreateInput) (live.Live, error)
	CreateAndJoin(ctx context.Context, in live.CreateInput, identity string) (live.Live, live.Join, error)
	List(ctx context.Context) ([]live.Live, error)
	Get(ctx context.Context, liveID string) (live.Live, error)
	Join(ctx context.Context, liveID, userID, identity string) (live.Join, error)
	End(ctx context.Context, liveID string) error
	Leave(ctx context.Context, liveID string) error
	SFUReady() bool
}

// Live menangani endpoint siaran langsung.
type Live struct {
	svc   LiveService
	media MediaURLResolver
}

// NewLive membuat handler live.
func NewLive(svc LiveService, media MediaURLResolver) *Live {
	return &Live{svc: svc, media: media}
}

type liveDTO struct {
	ID            string `json:"id"`
	HostID        string `json:"host_id"`
	HostUsername  string `json:"host_username,omitempty"`
	HostName      string `json:"host_name,omitempty"`
	HostAvatarURL string `json:"host_avatar_url,omitempty"`

	Title    string `json:"title"`
	Category string `json:"category,omitempty"`

	ViewerCount int       `json:"viewer_count"`
	StartedAt   time.Time `json:"started_at"`
}

func (h *Live) toLiveDTO(l live.Live) liveDTO {
	return liveDTO{
		ID:            l.ID,
		HostID:        l.HostID,
		HostUsername:  l.HostUsername,
		HostName:      l.HostName,
		HostAvatarURL: h.media.PublicURL(l.HostAvatarID),
		Title:         l.Title,
		Category:      l.Category,
		ViewerCount:   l.ViewerCount,
		StartedAt:     l.StartedAt,
	}
}

type liveJoinDTO struct {
	LiveID     string `json:"live_id"`
	Role       string `json:"role,omitempty"`
	CanPublish bool   `json:"can_publish"`

	// Dua field inilah yang membuat video benar-benar mengalir. Host memakai
	// sfu_token untuk publish kamera; penonton untuk subscribe track host.
	// Kalau sfu_token kosong, media server belum dikonfigurasi.
	SFURoomID string `json:"sfu_room_id"`
	SFUToken  string `json:"sfu_token,omitempty"`
	SFUURL    string `json:"sfu_url,omitempty"`
}

func toLiveJoinDTO(j live.Join) liveJoinDTO {
	return liveJoinDTO{
		LiveID:     j.LiveID,
		Role:       string(j.Role),
		CanPublish: j.CanPublish,
		SFURoomID:  j.SFURoomID,
		SFUToken:   j.SFUToken,
		SFUURL:     j.SFUURL,
	}
}

// List menangani GET /api/v1/lives — daftar siaran yang sedang berlangsung.
func (h *Live) List(w http.ResponseWriter, r *http.Request) {
	lives, err := h.svc.List(r.Context())
	if err != nil {
		writeLiveError(w, r, err)
		return
	}

	items := make([]liveDTO, 0, len(lives))
	for _, l := range lives {
		items = append(items, h.toLiveDTO(l))
	}

	httpx.Page(w, items, map[string]any{
		"count":     len(items),
		"sfu_ready": h.svc.SFUReady(),
	})
}

type createLiveRequest struct {
	Title    string `json:"title"`
	Category string `json:"category,omitempty"`
}

type createdLiveDTO struct {
	liveDTO

	// Join disertakan supaya host langsung tersambung dan bisa menyiarkan kamera
	// tanpa perlu memanggil /join secara terpisah.
	Join liveJoinDTO `json:"join"`
}

// Create menangani POST /api/v1/lives — membuat live DAN langsung memasukkan
// pembuatnya sebagai host lengkap dengan token SFU untuk menyiarkan kamera.
func (h *Live) Create(w http.ResponseWriter, r *http.Request) {
	var req createLiveRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	principal, _ := auth.FromContext(r.Context())

	created, joined, err := h.svc.CreateAndJoin(r.Context(), live.CreateInput{
		HostID:   principal.UserID,
		Title:    req.Title,
		Category: req.Category,
	}, principal.UserID)
	if err != nil {
		writeLiveError(w, r, err)
		return
	}

	httpx.Created(w, createdLiveDTO{
		liveDTO: h.toLiveDTO(created),
		Join:    toLiveJoinDTO(joined),
	})
}

// Get menangani GET /api/v1/lives/{id} — dipakai penonton memeriksa apakah
// siaran masih hidup (404 = sudah berakhir, tutup layar).
func (h *Live) Get(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLiveError(w, r, err)
		return
	}
	httpx.OK(w, h.toLiveDTO(l))
}

// Join menangani POST /api/v1/lives/{id}/join.
func (h *Live) Join(w http.ResponseWriter, r *http.Request) {
	liveID := r.PathValue("id")
	if liveID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id live tidak boleh kosong")
		return
	}

	principal, _ := auth.FromContext(r.Context())

	joined, err := h.svc.Join(r.Context(), liveID, principal.UserID, principal.UserID)
	if err != nil {
		writeLiveError(w, r, err)
		return
	}
	httpx.OK(w, toLiveJoinDTO(joined))
}

// End menangani POST /api/v1/lives/{id}/end dan DELETE /api/v1/lives/{id}.
// Hanya host.
func (h *Live) End(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.End(r.Context(), r.PathValue("id")); err != nil {
		writeLiveError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Leave menangani POST /api/v1/lives/{id}/leave — penonton keluar; kalau host
// yang keluar, live berakhir.
func (h *Live) Leave(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Leave(r.Context(), r.PathValue("id")); err != nil {
		writeLiveError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func writeLiveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, live.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "live tidak ditemukan")

	case errors.Is(err, live.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "tidak diizinkan")

	case errors.Is(err, live.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
