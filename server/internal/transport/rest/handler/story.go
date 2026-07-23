package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/story"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// StoryService adalah bagian domain story yang dipakai handler REST.
type StoryService interface {
	Create(ctx context.Context, userID, mediaID string, visibility story.Visibility) (story.Story, error)
	ListGrouped(ctx context.Context, userID string) ([]story.Group, error)
	MarkViewed(ctx context.Context, storyID, userID string) error
}

// MediaURLResolver menerjemahkan storage key menjadi URL yang bisa dibuka klien.
type MediaURLResolver interface {
	PublicURL(storageKey string) string
}

// Story menangani endpoint story.
type Story struct {
	svc   StoryService
	media MediaURLResolver
}

// NewStory membuat handler story.
func NewStory(svc StoryService, media MediaURLResolver) *Story {
	return &Story{svc: svc, media: media}
}

type storyDTO struct {
	ID         string    `json:"id"`
	MediaID    string    `json:"media_id"`
	MediaKind  string    `json:"media_kind"`
	MediaURL   string    `json:"media_url"`
	DurationMs int       `json:"duration_ms,omitempty"`
	Viewed     bool      `json:"viewed"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// storyGroupDTO adalah bentuk yang langsung dipakai story row di aplikasi:
// satu grup = satu avatar, len(stories) = jumlah segmen ring, all_viewed
// menentukan ring berwarna atau abu.
type storyGroupDTO struct {
	AuthorID      string     `json:"author_id"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"display_name"`
	AvatarMediaID string     `json:"avatar_media_id,omitempty"`
	IsCurrentUser bool       `json:"is_current_user"`
	AllViewed     bool       `json:"all_viewed"`
	UnviewedCount int        `json:"unviewed_count"`
	LatestStoryAt time.Time  `json:"latest_story_at"`
	Stories       []storyDTO `json:"stories"`
}

// List menangani GET /api/v1/stories.
//
// Hasilnya sudah dikelompokkan per orang dan diurutkan — story milik sendiri
// di depan, persis seperti tampilan story row. Klien tidak perlu
// mengelompokkan atau mengurutkan apa pun.
func (h *Story) List(w http.ResponseWriter, r *http.Request) {
	groups, err := h.svc.ListGrouped(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeStoryError(w, r, err)
		return
	}

	items := make([]storyGroupDTO, 0, len(groups))
	for _, g := range groups {
		stories := make([]storyDTO, 0, len(g.Stories))
		for _, s := range g.Stories {
			stories = append(stories, storyDTO{
				ID:         s.ID,
				MediaID:    s.MediaID,
				MediaKind:  s.MediaKind,
				MediaURL:   h.media.PublicURL(s.StorageKey),
				DurationMs: s.DurationMs,
				Viewed:     s.Viewed,
				CreatedAt:  s.CreatedAt,
				ExpiresAt:  s.ExpiresAt,
			})
		}

		items = append(items, storyGroupDTO{
			AuthorID:      g.AuthorID,
			Username:      g.Username,
			DisplayName:   g.DisplayName,
			AvatarMediaID: g.AvatarMediaID,
			IsCurrentUser: g.IsCurrentUser,
			AllViewed:     g.AllViewed,
			UnviewedCount: g.UnviewedCount,
			LatestStoryAt: g.LatestStoryAt,
			Stories:       stories,
		})
	}

	httpx.OK(w, items)
}

type createStoryRequest struct {
	MediaID    string `json:"media_id"`
	Visibility string `json:"visibility,omitempty"`
}

// Create menangani POST /api/v1/stories.
//
// Media harus sudah diunggah dan dikonfirmasi lebih dulu lewat alur di
// handler/media.go — story hanya menunjuk ke media, tidak membawa byte-nya.
func (h *Story) Create(w http.ResponseWriter, r *http.Request) {
	var req createStoryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	created, err := h.svc.Create(
		r.Context(),
		auth.UserID(r.Context()),
		req.MediaID,
		story.Visibility(req.Visibility),
	)
	if err != nil {
		writeStoryError(w, r, err)
		return
	}

	httpx.Created(w, storyDTO{
		ID:        created.ID,
		MediaID:   created.MediaID,
		CreatedAt: created.CreatedAt,
		ExpiresAt: created.ExpiresAt,
	})
}

// MarkViewed menangani POST /api/v1/stories/{id}/view.
//
// Idempoten: menonton ulang tidak menaikkan counter untuk kedua kalinya.
func (h *Story) MarkViewed(w http.ResponseWriter, r *http.Request) {
	storyID := r.PathValue("id")
	if storyID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id story tidak boleh kosong")
		return
	}

	if err := h.svc.MarkViewed(r.Context(), storyID, auth.UserID(r.Context())); err != nil {
		writeStoryError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

func writeStoryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, story.ErrMediaNotOwn):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "media bukan milik kamu")

	case errors.Is(err, story.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "story tidak ditemukan")

	case errors.Is(err, story.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
