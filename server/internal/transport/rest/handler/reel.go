package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/reel"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// ReelService adalah bagian domain reel yang dipakai handler REST.
type ReelService interface {
	Create(ctx context.Context, in reel.CreateInput) (reel.Reel, error)
	Feed(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error)
	Get(ctx context.Context, reelID, userID string) (reel.Reel, error)
	ListMine(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error)
	ListByUser(ctx context.Context, username, userID string, before reel.Cursor, limit int) ([]reel.Reel, error)
	ListSaved(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error)
	Delete(ctx context.Context, reelID, userID string) error
	Like(ctx context.Context, reelID, userID string) error
	Unlike(ctx context.Context, reelID, userID string) error
	Save(ctx context.Context, reelID, userID string) error
	Unsave(ctx context.Context, reelID, userID string) error
	RecordView(ctx context.Context, reelID, userID string) error
	AddComment(ctx context.Context, reelID, userID, body, parentID string) (reel.Comment, error)
	ListComments(ctx context.Context, reelID, userID string, before reel.Cursor, limit int) ([]reel.Comment, error)
	DeleteComment(ctx context.Context, commentID, userID string) error
}

// Reel menangani endpoint reels / shorts.
type Reel struct {
	svc   ReelService
	media MediaURLResolver
}

// NewReel membuat handler reel.
func NewReel(svc ReelService, media MediaURLResolver) *Reel {
	return &Reel{svc: svc, media: media}
}

type reelDTO struct {
	ID              string    `json:"id"`
	AuthorID        string    `json:"author_id"`
	AuthorUsername  string    `json:"author_username"`
	AuthorName      string    `json:"author_name"`
	AuthorAvatarURL string    `json:"author_avatar_url,omitempty"`
	MediaID         string    `json:"media_id"`
	MediaKind       string    `json:"media_kind"`
	MediaURL        string    `json:"media_url"`
	DurationMs      int       `json:"duration_ms,omitempty"`
	Caption         string    `json:"caption"`
	Visibility      string    `json:"visibility"`
	CommentsEnabled bool      `json:"comments_enabled"`
	LikeCount       int       `json:"like_count"`
	CommentCount    int       `json:"comment_count"`
	ViewCount       int       `json:"view_count"`
	ShareCount      int       `json:"share_count"`
	Liked           bool      `json:"liked"`
	Saved           bool      `json:"saved"`
	PublishedAt     time.Time `json:"published_at"`
}

// reelCursorMeta membawa cursor gabungan waktu+id untuk halaman berikutnya.
type reelCursorMeta struct {
	Count        int        `json:"count"`
	NextBeforeAt *time.Time `json:"next_before_at,omitempty"`
	NextBeforeID string     `json:"next_before_id,omitempty"`
}

func (h *Reel) toReelDTO(r reel.Reel) reelDTO {
	// author_avatar dari SQL adalah media id, bukan storage key — klien
	// menyusun URL avatar lewat endpoint profil, jadi di sini dibiarkan kosong.
	return reelDTO{
		ID:              r.ID,
		AuthorID:        r.AuthorID,
		AuthorUsername:  r.AuthorUsername,
		AuthorName:      firstNonEmptyStr(r.AuthorName, r.AuthorUsername),
		MediaID:         r.MediaID,
		MediaKind:       r.MediaKind,
		MediaURL:        h.media.PublicURL(r.StorageKey),
		DurationMs:      r.DurationMs,
		Caption:         r.Caption,
		Visibility:      string(r.Visibility),
		CommentsEnabled: r.CommentsEnabled,
		LikeCount:       r.LikeCount,
		CommentCount:    r.CommentCount,
		ViewCount:       r.ViewCount,
		ShareCount:      r.ShareCount,
		Liked:           r.Liked,
		Saved:           r.Saved,
		PublishedAt:     r.PublishedAt,
	}
}

func (h *Reel) writePage(w http.ResponseWriter, r *http.Request, items []reel.Reel) {
	dtos := make([]reelDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, h.toReelDTO(it))
	}
	meta := reelCursorMeta{Count: len(dtos)}
	if n := len(items); n > 0 {
		last := items[n-1]
		meta.NextBeforeAt = &last.PublishedAt
		meta.NextBeforeID = last.ID
	}
	httpx.Page(w, dtos, meta)
}

// parseReelCursor membaca before_at (RFC3339) + before_id dari query.
func parseReelCursor(r *http.Request) (reel.Cursor, bool) {
	raw := r.URL.Query().Get("before_at")
	if raw == "" {
		return reel.Cursor{}, true
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return reel.Cursor{}, false
	}
	return reel.Cursor{At: at, ID: r.URL.Query().Get("before_id")}, true
}

func reelLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

// Feed menangani GET /api/v1/reels.
func (h *Reel) Feed(w http.ResponseWriter, r *http.Request) {
	cursor, ok := parseReelCursor(r)
	if !ok {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "before_at harus RFC3339")
		return
	}
	items, err := h.svc.Feed(r.Context(), auth.UserID(r.Context()), cursor, reelLimit(r))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	h.writePage(w, r, items)
}

type createReelRequest struct {
	MediaID         string `json:"media_id"`
	Caption         string `json:"caption"`
	Visibility      string `json:"visibility"`
	CommentsEnabled *bool  `json:"comments_enabled"`
}

// Create menangani POST /api/v1/reels.
func (h *Reel) Create(w http.ResponseWriter, r *http.Request) {
	var req createReelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	commentsEnabled := true
	if req.CommentsEnabled != nil {
		commentsEnabled = *req.CommentsEnabled
	}
	created, err := h.svc.Create(r.Context(), reel.CreateInput{
		UserID:          auth.UserID(r.Context()),
		MediaID:         req.MediaID,
		Caption:         req.Caption,
		Visibility:      reel.Visibility(req.Visibility),
		CommentsEnabled: commentsEnabled,
	})
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.Created(w, h.toReelDTO(created))
}

// Get menangani GET /api/v1/reels/{id}.
func (h *Reel) Get(w http.ResponseWriter, r *http.Request) {
	rl, err := h.svc.Get(r.Context(), r.PathValue("id"), auth.UserID(r.Context()))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.OK(w, h.toReelDTO(rl))
}

// ListMine menangani GET /api/v1/reels/me.
func (h *Reel) ListMine(w http.ResponseWriter, r *http.Request) {
	cursor, ok := parseReelCursor(r)
	if !ok {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "before_at harus RFC3339")
		return
	}
	items, err := h.svc.ListMine(r.Context(), auth.UserID(r.Context()), cursor, reelLimit(r))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	h.writePage(w, r, items)
}

// ListByUser menangani GET /api/v1/users/{username}/reels.
func (h *Reel) ListByUser(w http.ResponseWriter, r *http.Request) {
	cursor, ok := parseReelCursor(r)
	if !ok {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "before_at harus RFC3339")
		return
	}
	items, err := h.svc.ListByUser(r.Context(), r.PathValue("username"), auth.UserID(r.Context()), cursor, reelLimit(r))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	h.writePage(w, r, items)
}

// ListSaved menangani GET /api/v1/reels/saved.
func (h *Reel) ListSaved(w http.ResponseWriter, r *http.Request) {
	cursor, ok := parseReelCursor(r)
	if !ok {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "before_at harus RFC3339")
		return
	}
	items, err := h.svc.ListSaved(r.Context(), auth.UserID(r.Context()), cursor, reelLimit(r))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	h.writePage(w, r, items)
}

// Delete menangani DELETE /api/v1/reels/{id}.
func (h *Reel) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Like menangani PUT /api/v1/reels/{id}/like.
func (h *Reel) Like(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Like(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Unlike menangani DELETE /api/v1/reels/{id}/like.
func (h *Reel) Unlike(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Unlike(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Save menangani PUT /api/v1/reels/{id}/save.
func (h *Reel) Save(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Save(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Unsave menangani DELETE /api/v1/reels/{id}/save.
func (h *Reel) Unsave(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Unsave(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// RecordView menangani POST /api/v1/reels/{id}/view.
//
// Idempoten & di-dedup di database: satu penonton dihitung sekali, tayangan
// ulang tidak menambah baris. Klien boleh memanggilnya tiap kali reel tampil.
func (h *Reel) RecordView(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RecordView(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type reelCommentDTO struct {
	ID              string    `json:"id"`
	ReelID          string    `json:"reel_id"`
	AuthorID        string    `json:"author_id"`
	AuthorUsername  string    `json:"author_username"`
	AuthorName      string    `json:"author_name"`
	ParentCommentID string    `json:"parent_comment_id,omitempty"`
	Body            string    `json:"body"`
	LikeCount       int       `json:"like_count"`
	CreatedAt       time.Time `json:"created_at"`
}

func toReelCommentDTO(c reel.Comment) reelCommentDTO {
	return reelCommentDTO{
		ID:              c.ID,
		ReelID:          c.ReelID,
		AuthorID:        c.AuthorID,
		AuthorUsername:  c.AuthorUsername,
		AuthorName:      firstNonEmptyStr(c.AuthorName, c.AuthorUsername),
		ParentCommentID: c.ParentCommentID,
		Body:            c.Body,
		LikeCount:       c.LikeCount,
		CreatedAt:       c.CreatedAt,
	}
}

type addCommentRequest struct {
	Body     string `json:"body"`
	ParentID string `json:"parent_id"`
}

// AddComment menangani POST /api/v1/reels/{id}/comments.
func (h *Reel) AddComment(w http.ResponseWriter, r *http.Request) {
	var req addCommentRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	c, err := h.svc.AddComment(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), req.Body, req.ParentID)
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.Created(w, toReelCommentDTO(c))
}

type reelCommentPageMeta struct {
	Count        int        `json:"count"`
	NextBeforeAt *time.Time `json:"next_before_at,omitempty"`
	NextBeforeID string     `json:"next_before_id,omitempty"`
}

// ListComments menangani GET /api/v1/reels/{id}/comments.
func (h *Reel) ListComments(w http.ResponseWriter, r *http.Request) {
	cursor, ok := parseReelCursor(r)
	if !ok {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "before_at harus RFC3339")
		return
	}
	items, err := h.svc.ListComments(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), cursor, reelLimit(r))
	if err != nil {
		writeReelError(w, r, err)
		return
	}
	dtos := make([]reelCommentDTO, 0, len(items))
	for _, c := range items {
		dtos = append(dtos, toReelCommentDTO(c))
	}
	meta := reelCommentPageMeta{Count: len(dtos)}
	if n := len(items); n > 0 {
		last := items[n-1]
		meta.NextBeforeAt = &last.CreatedAt
		meta.NextBeforeID = last.ID
	}
	httpx.Page(w, dtos, meta)
}

// DeleteComment menangani DELETE /api/v1/reels/comments/{comment_id}.
func (h *Reel) DeleteComment(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteComment(r.Context(), r.PathValue("comment_id"), auth.UserID(r.Context())); err != nil {
		writeReelError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// firstNonEmptyStr mengembalikan argumen non-kosong pertama.
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func writeReelError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reel.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "reel tidak ditemukan")
	case errors.Is(err, reel.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "aksi tidak diizinkan")
	case errors.Is(err, reel.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "input tidak valid")
	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
