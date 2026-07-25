package supabase

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/reel"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// ReelRepository memenuhi kontrak reel.Repository.
type ReelRepository struct {
	client *sb.Client
}

// NewReelRepository membuat repository reel.
func NewReelRepository(client *sb.Client) *ReelRepository {
	return &ReelRepository{client: client}
}

var _ reel.Repository = (*ReelRepository)(nil)

// reelRow memetakan kolom yang dikembalikan fungsi-fungsi feed reel.
type reelRow struct {
	ID              string    `json:"id"`
	AuthorID        string    `json:"author_id"`
	AuthorUsername  string    `json:"author_username"`
	AuthorName      string    `json:"author_name"`
	AuthorAvatar    *string   `json:"author_avatar"`
	MediaID         string    `json:"media_id"`
	MediaKind       string    `json:"media_kind"`
	StorageKey      string    `json:"storage_key"`
	DurationMs      *int      `json:"duration_ms"`
	Caption         string    `json:"caption"`
	Visibility      string    `json:"visibility"`
	CommentsEnabled bool      `json:"comments_enabled"`
	LikeCount       int       `json:"like_count"`
	CommentCount    int       `json:"comment_count"`
	ViewCount       int       `json:"view_count"`
	ShareCount      int       `json:"share_count"`
	Liked           bool      `json:"liked"`
	Saved           bool      `json:"saved"`
	IsFollowing     bool      `json:"is_following"`
	PublishedAt     time.Time `json:"published_at"`
}

func (row reelRow) toDomain() reel.Reel {
	dur := 0
	if row.DurationMs != nil {
		dur = *row.DurationMs
	}
	return reel.Reel{
		ID:              row.ID,
		AuthorID:        row.AuthorID,
		AuthorUsername:  row.AuthorUsername,
		AuthorName:      row.AuthorName,
		AuthorAvatarID:  deref(row.AuthorAvatar),
		MediaID:         row.MediaID,
		MediaKind:       row.MediaKind,
		StorageKey:      row.StorageKey,
		DurationMs:      dur,
		Caption:         row.Caption,
		Visibility:      reel.Visibility(row.Visibility),
		CommentsEnabled: row.CommentsEnabled,
		LikeCount:       row.LikeCount,
		CommentCount:    row.CommentCount,
		ViewCount:       row.ViewCount,
		ShareCount:      row.ShareCount,
		Liked:           row.Liked,
		Saved:           row.Saved,
		IsFollowing:     row.IsFollowing,
		PublishedAt:     row.PublishedAt,
	}
}

func cursorArgs(before reel.Cursor) (any, any) {
	if before.IsZero() {
		return nil, nil
	}
	return before.At.UTC(), nullIfEmpty(before.ID)
}

// Create memanggil create_reel.
func (r *ReelRepository) Create(ctx context.Context, rl reel.Reel) error {
	actor, err := actorOption(ctx, rl.AuthorID)
	if err != nil {
		return err
	}
	args := map[string]any{
		"p_id":               rl.ID,
		"p_media":            rl.MediaID,
		"p_caption":          rl.Caption,
		"p_visibility":       string(rl.Visibility),
		"p_comments_enabled": rl.CommentsEnabled,
		"p_published_at":     rl.PublishedAt.UTC(),
	}
	if err := r.client.RPC(ctx, "create_reel", args, nil, actor); err != nil {
		return translateReel(err)
	}
	return nil
}

// Feed memanggil list_reels_feed.
func (r *ReelRepository) Feed(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	at, id := cursorArgs(before)
	var rows []reelRow
	if err := r.client.RPC(ctx, "list_reels_feed",
		map[string]any{"p_limit": limit, "p_before_at": at, "p_before_id": id}, &rows, actor); err != nil {
		return nil, translateReel(err)
	}
	return reelsToDomain(rows), nil
}

// Get memanggil get_reel.
func (r *ReelRepository) Get(ctx context.Context, reelID, userID string) (reel.Reel, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return reel.Reel{}, err
	}
	var rows []reelRow
	if err := r.client.RPC(ctx, "get_reel", map[string]any{"p_reel": reelID}, &rows, actor); err != nil {
		return reel.Reel{}, translateReel(err)
	}
	if len(rows) == 0 {
		return reel.Reel{}, reel.ErrNotFound
	}
	return rows[0].toDomain(), nil
}

// Mine memanggil list_my_reels.
func (r *ReelRepository) Mine(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	at, id := cursorArgs(before)
	var rows []reelRow
	if err := r.client.RPC(ctx, "list_my_reels",
		map[string]any{"p_limit": limit, "p_before_at": at, "p_before_id": id}, &rows, actor); err != nil {
		return nil, translateReel(err)
	}
	return reelsToDomain(rows), nil
}

// ListByUser memanggil list_user_reels.
func (r *ReelRepository) ListByUser(ctx context.Context, username, userID string, before reel.Cursor, limit int) ([]reel.Reel, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	at, id := cursorArgs(before)
	var rows []reelRow
	if err := r.client.RPC(ctx, "list_user_reels",
		map[string]any{"p_username": username, "p_limit": limit, "p_before_at": at, "p_before_id": id}, &rows, actor); err != nil {
		return nil, translateReel(err)
	}
	return reelsToDomain(rows), nil
}

// ListSaved memanggil list_saved_reels.
func (r *ReelRepository) ListSaved(ctx context.Context, userID string, before reel.Cursor, limit int) ([]reel.Reel, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	at, id := cursorArgs(before)
	var rows []reelRow
	if err := r.client.RPC(ctx, "list_saved_reels",
		map[string]any{"p_limit": limit, "p_before_at": at, "p_before_id": id}, &rows, actor); err != nil {
		return nil, translateReel(err)
	}
	return reelsToDomain(rows), nil
}

// Delete memanggil delete_reel.
func (r *ReelRepository) Delete(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "delete_reel", map[string]any{"p_reel": reelID})
}

// Like memanggil like_reel.
func (r *ReelRepository) Like(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "like_reel", map[string]any{"p_reel": reelID})
}

// Unlike memanggil unlike_reel.
func (r *ReelRepository) Unlike(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "unlike_reel", map[string]any{"p_reel": reelID})
}

// Save memanggil save_reel.
func (r *ReelRepository) Save(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "save_reel", map[string]any{"p_reel": reelID})
}

// Unsave memanggil unsave_reel.
func (r *ReelRepository) Unsave(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "unsave_reel", map[string]any{"p_reel": reelID})
}

// RecordView memanggil record_reel_view.
func (r *ReelRepository) RecordView(ctx context.Context, reelID, userID string) error {
	return r.void(ctx, userID, "record_reel_view", map[string]any{"p_reel": reelID})
}

type reelCommentRow struct {
	ID              string    `json:"id"`
	ReelID          string    `json:"reel_id"`
	AuthorID        string    `json:"author_id"`
	AuthorUsername  string    `json:"author_username"`
	AuthorName      string    `json:"author_name"`
	AuthorAvatar    *string   `json:"author_avatar"`
	ParentCommentID *string   `json:"parent_comment_id"`
	Body            string    `json:"body"`
	LikeCount       int       `json:"like_count"`
	CreatedAt       time.Time `json:"created_at"`
}

// AddComment memanggil add_reel_comment.
func (r *ReelRepository) AddComment(ctx context.Context, c reel.Comment) error {
	actor, err := actorOption(ctx, c.AuthorID)
	if err != nil {
		return err
	}
	args := map[string]any{
		"p_id":         c.ID,
		"p_reel":       c.ReelID,
		"p_body":       c.Body,
		"p_parent":     nullIfEmpty(c.ParentCommentID),
		"p_created_at": c.CreatedAt.UTC(),
	}
	if err := r.client.RPC(ctx, "add_reel_comment", args, nil, actor); err != nil {
		return translateReel(err)
	}
	return nil
}

// CommentAuthor mengembalikan id penulis sebuah komentar (untuk memberi tahu dia
// saat komentarnya dibalas). Memakai service role: RLS reel_comments membatasi
// SELECT, dan yang dikembalikan hanya satu uuid untuk keperluan notifikasi —
// bukan konten. Kembalikan string kosong kalau komentarnya tidak ada.
func (r *ReelRepository) CommentAuthor(ctx context.Context, commentID string) (string, error) {
	q := url.Values{}
	q.Set("select", "author_id")
	q.Set("id", "eq."+commentID)
	q.Set("limit", "1")
	var rows []struct {
		AuthorID string `json:"author_id"`
	}
	if err := r.client.Select(ctx, "reel_comments", &rows, sb.WithServiceRole(), sb.WithQuery(q)); err != nil {
		return "", translateReel(err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].AuthorID, nil
}

// ListComments memanggil list_reel_comments.
func (r *ReelRepository) ListComments(ctx context.Context, reelID, userID string, before reel.Cursor, limit int) ([]reel.Comment, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	at, id := cursorArgs(before)
	var rows []reelCommentRow
	if err := r.client.RPC(ctx, "list_reel_comments",
		map[string]any{"p_reel": reelID, "p_limit": limit, "p_before_at": at, "p_before_id": id}, &rows, actor); err != nil {
		return nil, translateReel(err)
	}
	out := make([]reel.Comment, 0, len(rows))
	for _, row := range rows {
		out = append(out, reel.Comment{
			ID:              row.ID,
			ReelID:          row.ReelID,
			AuthorID:        row.AuthorID,
			AuthorUsername:  row.AuthorUsername,
			AuthorName:      row.AuthorName,
			AuthorAvatarID:  deref(row.AuthorAvatar),
			ParentCommentID: deref(row.ParentCommentID),
			Body:            row.Body,
			LikeCount:       row.LikeCount,
			CreatedAt:       row.CreatedAt,
		})
	}
	return out, nil
}

// DeleteComment memanggil delete_reel_comment.
func (r *ReelRepository) DeleteComment(ctx context.Context, commentID, userID string) error {
	return r.void(ctx, userID, "delete_reel_comment", map[string]any{"p_comment": commentID})
}

func (r *ReelRepository) void(ctx context.Context, userID, fn string, args map[string]any) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, fn, args, nil, actor); err != nil {
		return translateReel(err)
	}
	return nil
}

func reelsToDomain(rows []reelRow) []reel.Reel {
	out := make([]reel.Reel, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out
}

func translateReel(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return reel.ErrNotAllowed
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return reel.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return reel.ErrInvalidInput
	default:
		return err
	}
}
