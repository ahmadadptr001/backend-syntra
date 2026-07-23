package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/notification"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// NotificationService adalah bagian domain notification yang dipakai handler.
type NotificationService interface {
	List(ctx context.Context, before string, limit int) ([]notification.Notification, error)
	CountUnread(ctx context.Context) (int, error)
	MarkRead(ctx context.Context, notificationID string) (int, error)
}

// Notification menangani endpoint pemberitahuan.
type Notification struct {
	svc   NotificationService
	media MediaURLResolver
}

// NewNotification membuat handler notifikasi.
func NewNotification(svc NotificationService, media MediaURLResolver) *Notification {
	return &Notification{svc: svc, media: media}
}

type notificationDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	ActorID        string `json:"actor_id,omitempty"`
	ActorUsername  string `json:"actor_username,omitempty"`
	ActorName      string `json:"actor_name,omitempty"`
	ActorAvatarURL string `json:"actor_avatar_url,omitempty"`

	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`

	IsRead    bool      `json:"is_read"`
	CreatedAt time.Time `json:"created_at"`
}

type notificationPageMeta struct {
	Count      int    `json:"count"`
	NextBefore string `json:"next_before,omitempty"`
}

// List menangani GET /api/v1/notifications.
//
// Terbaru dulu. Cursor `before` adalah id notifikasi — id memakai UUIDv7 yang
// terurut waktu, jadi cursor berbasis id tidak melewatkan baris ketika dua
// notifikasi lahir pada milidetik yang sama.
func (h *Notification) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0 // service yang menentukan bawaan dan batas atas
	}

	items, err := h.svc.List(r.Context(), r.URL.Query().Get("before"), limit)
	if err != nil {
		writeNotificationError(w, r, err)
		return
	}

	out := make([]notificationDTO, 0, len(items))
	for _, n := range items {
		out = append(out, notificationDTO{
			ID:             n.ID,
			Type:           string(n.Type),
			ActorID:        n.ActorID,
			ActorUsername:  n.ActorUsername,
			ActorName:      n.ActorName,
			ActorAvatarURL: h.media.PublicURL(n.ActorAvatarKey),
			SubjectType:    n.SubjectType,
			SubjectID:      n.SubjectID,
			IsRead:         n.IsRead,
			CreatedAt:      n.CreatedAt,
		})
	}

	meta := notificationPageMeta{Count: len(out)}
	if n := len(out); n > 0 {
		meta.NextBefore = out[n-1].ID
	}

	httpx.Page(w, out, meta)
}

// UnreadCount menangani GET /api/v1/notifications/unread-count.
//
// Endpoint terpisah karena badge dipanggil jauh lebih sering daripada
// daftarnya, dan jawabannya jauh lebih kecil.
func (h *Notification) UnreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.CountUnread(r.Context())
	if err != nil {
		writeNotificationError(w, r, err)
		return
	}
	httpx.OK(w, map[string]int{"unread": n})
}

type markReadRequest struct {
	// Kosong berarti tandai semua.
	NotificationID string `json:"notification_id,omitempty"`
}

// MarkRead menangani POST /api/v1/notifications/read.
func (h *Notification) MarkRead(w http.ResponseWriter, r *http.Request) {
	var req markReadRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	n, err := h.svc.MarkRead(r.Context(), req.NotificationID)
	if err != nil {
		writeNotificationError(w, r, err)
		return
	}
	httpx.OK(w, map[string]int{"marked": n})
}

func writeNotificationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, notification.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "notifikasi tidak ditemukan")

	case errors.Is(err, notification.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
