package supabase

import (
	"context"
	"errors"
	"net/url"
	"strings"
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

// avatarKeys menerjemahkan id media avatar menjadi storage_key dalam satu query.
//
// Fungsi feed reel (dan komentar) mengembalikan avatar penulis sebagai id media
// (uuid), bukan storage_key — sehingga backend tak bisa menyusun URL dan avatar
// jatuh ke inisial. media_assets bisa dibaca semua pengguna terautentikasi
// (policy media_select USING true), jadi token pemanggil cukup. Id yang tak
// ditemukan sekadar absen dari peta. Best effort: kegagalan → peta kosong.
func (r *ReelRepository) avatarKeys(ctx context.Context, userID string, ids []string) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return out
	}
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return out
	}
	q := url.Values{}
	q.Set("select", "id,storage_key")
	q.Set("id", "in.("+strings.Join(uniq, ",")+")")
	var rows []struct {
		ID         string `json:"id"`
		StorageKey string `json:"storage_key"`
	}
	if err := r.client.Select(ctx, "media_assets", &rows, actor, sb.WithQuery(q)); err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ID] = row.StorageKey
	}
	return out
}

// hydrateReelAvatars mengganti AuthorAvatarID tiap reel (semula id media) dengan
// storage_key-nya, supaya handler bisa memanggil PublicURL. Reel tanpa avatar
// menjadi string kosong.
func (r *ReelRepository) hydrateReelAvatars(ctx context.Context, userID string, reels []reel.Reel) {
	ids := make([]string, 0, len(reels))
	for _, rl := range reels {
		ids = append(ids, rl.AuthorAvatarID)
	}
	keys := r.avatarKeys(ctx, userID, ids)
	for i := range reels {
		reels[i].AuthorAvatarID = keys[reels[i].AuthorAvatarID]
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
	out := reelsToDomain(rows)
	r.hydrateReelAvatars(ctx, userID, out)
	return out, nil
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
	out := []reel.Reel{rows[0].toDomain()}
	r.hydrateReelAvatars(ctx, userID, out)
	return out[0], nil
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
	out := reelsToDomain(rows)
	r.hydrateReelAvatars(ctx, userID, out)
	return out, nil
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
	out := reelsToDomain(rows)
	r.hydrateReelAvatars(ctx, userID, out)
	return out, nil
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
	out := reelsToDomain(rows)
	r.hydrateReelAvatars(ctx, userID, out)
	return out, nil
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
	// Resolve avatar media ids → storage keys (sama seperti reel), supaya handler
	// bisa menyusun URL foto profil penulis komentar.
	ids := make([]string, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.AuthorAvatarID)
	}
	keys := r.avatarKeys(ctx, userID, ids)
	for i := range out {
		out[i].AuthorAvatarID = keys[out[i].AuthorAvatarID]
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
