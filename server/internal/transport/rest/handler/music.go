package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/music"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// MusicService adalah bagian domain musik yang dipakai handler REST.
type MusicService interface {
	Create(ctx context.Context, in music.CreateInput) (music.Track, error)
	Feed(ctx context.Context, userID string, limit int) ([]music.Track, error)
	Search(ctx context.Context, userID, query string, limit int) ([]music.Track, error)
	Delete(ctx context.Context, trackID, userID string) error
}

// Music menangani endpoint katalog musik komunitas.
type Music struct {
	svc   MusicService
	media MediaURLResolver
}

// NewMusic membuat handler musik.
func NewMusic(svc MusicService, media MediaURLResolver) *Music {
	return &Music{svc: svc, media: media}
}

// musicDTO adalah bentuk yang dibaca aplikasi (SyntraClient.toCommunityTrack):
// url (audio), id, title, artist/author_name, cover_url, duration_ms.
type musicDTO struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	URL        string    `json:"url"`
	CoverURL   string    `json:"cover_url,omitempty"`
	DurationMs int       `json:"duration_ms"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	CreatedAt  time.Time `json:"created_at"`
}

func (h *Music) toDTO(t music.Track) musicDTO {
	return musicDTO{
		ID:         t.ID,
		Title:      t.Title,
		Artist:     t.Artist,
		URL:        h.media.PublicURL(t.StorageKey),
		CoverURL:   h.media.PublicURL(t.CoverStorageKey),
		DurationMs: t.DurationMs,
		AuthorID:   t.AuthorID,
		AuthorName: firstNonEmptyStr(t.AuthorName, t.AuthorUsername),
		CreatedAt:  t.CreatedAt,
	}
}

func (h *Music) writeList(w http.ResponseWriter, items []music.Track) {
	dtos := make([]musicDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, h.toDTO(it))
	}
	httpx.OK(w, dtos)
}

func musicLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

type createMusicRequest struct {
	MediaID      string `json:"media_id"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	DurationMs   int    `json:"duration_ms"`
	CoverMediaID string `json:"cover_media_id"`
	Visibility   string `json:"visibility"` // diterima tapi selalu publik untuk sekarang
}

// Create menangani POST /api/v1/music.
func (h *Music) Create(w http.ResponseWriter, r *http.Request) {
	var req createMusicRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	created, err := h.svc.Create(r.Context(), music.CreateInput{
		UserID:       auth.UserID(r.Context()),
		MediaID:      req.MediaID,
		CoverMediaID: req.CoverMediaID,
		Title:        req.Title,
		Artist:       req.Artist,
		DurationMs:   req.DurationMs,
	})
	if err != nil {
		writeMusicError(w, r, err)
		return
	}
	httpx.Created(w, h.toDTO(created))
}

// Feed menangani GET /api/v1/music.
func (h *Music) Feed(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Feed(r.Context(), auth.UserID(r.Context()), musicLimit(r))
	if err != nil {
		writeMusicError(w, r, err)
		return
	}
	h.writeList(w, items)
}

// Search menangani GET /api/v1/music/search?q=...
func (h *Music) Search(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Search(r.Context(), auth.UserID(r.Context()), r.URL.Query().Get("q"), musicLimit(r))
	if err != nil {
		writeMusicError(w, r, err)
		return
	}
	h.writeList(w, items)
}

// Delete menangani DELETE /api/v1/music/{id}.
func (h *Music) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeMusicError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func writeMusicError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, music.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "lagu tidak ditemukan")
	case errors.Is(err, music.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "aksi tidak diizinkan")
	case errors.Is(err, music.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "input tidak valid")
	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
