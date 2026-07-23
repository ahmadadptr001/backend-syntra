package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/story"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// StoryRepository memenuhi kontrak story.Repository.
type StoryRepository struct {
	client *sb.Client
}

// NewStoryRepository membuat repository story.
func NewStoryRepository(client *sb.Client) *StoryRepository {
	return &StoryRepository{client: client}
}

var _ story.Repository = (*StoryRepository)(nil)

// storyRow memetakan kolom yang dikembalikan fungsi list_stories.
type storyRow struct {
	ID             string    `json:"id"`
	AuthorID       string    `json:"author_id"`
	AuthorUsername string    `json:"author_username"`
	AuthorName     string    `json:"author_name"`
	AuthorAvatar   string    `json:"author_avatar_key"`
	MediaID        string    `json:"media_id"`
	MediaKind      string    `json:"media_kind"`
	StorageKey     string    `json:"storage_key"`
	DurationMs     *int      `json:"duration_ms"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Viewed         bool      `json:"viewed"`
}

// Create memanggil fungsi create_story.
func (r *StoryRepository) Create(ctx context.Context, s story.Story) error {
	actor, err := actorOption(ctx, s.AuthorID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_id":         s.ID,
		"p_media":      s.MediaID,
		"p_visibility": string(s.Visibility),
		"p_created_at": s.CreatedAt.UTC(),
	}

	if err := r.client.RPC(ctx, "create_story", args, nil, actor); err != nil {
		return translateStory(err)
	}
	return nil
}

// ListActive memanggil fungsi list_stories.
//
// Hasilnya sudah terurut per penulis dan per waktu dari database, sehingga
// pengelompokan di service hanya perlu satu lintasan tanpa sorting ulang.
func (r *StoryRepository) ListActive(ctx context.Context, userID string) ([]story.Story, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}

	var rows []storyRow
	if err := r.client.RPC(ctx, "list_stories", map[string]any{}, &rows, actor); err != nil {
		return nil, translateStory(err)
	}

	out := make([]story.Story, 0, len(rows))
	for _, row := range rows {
		duration := 0
		if row.DurationMs != nil {
			duration = *row.DurationMs
		}

		out = append(out, story.Story{
			ID:             row.ID,
			AuthorID:       row.AuthorID,
			AuthorUsername: row.AuthorUsername,
			AuthorName:     row.AuthorName,
			AuthorAvatarID: row.AuthorAvatar,
			MediaID:        row.MediaID,
			MediaKind:      row.MediaKind,
			StorageKey:     row.StorageKey,
			DurationMs:     duration,
			CreatedAt:      row.CreatedAt,
			ExpiresAt:      row.ExpiresAt,
			Viewed:         row.Viewed,
		})
	}
	return out, nil
}

type myStoryRow struct {
	ID         string    `json:"id"`
	MediaID    string    `json:"media_id"`
	MediaKind  string    `json:"media_kind"`
	StorageKey string    `json:"storage_key"`
	DurationMs *int      `json:"duration_ms"`
	ViewCount  int       `json:"view_count"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IsExpired  bool      `json:"is_expired"`
}

// ListMine memanggil fungsi list_my_stories.
func (r *StoryRepository) ListMine(ctx context.Context, userID string, includeExpired bool) ([]story.Mine, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}

	var rows []myStoryRow
	if err := r.client.RPC(ctx, "list_my_stories",
		map[string]any{"p_include_expired": includeExpired}, &rows, actor); err != nil {
		return nil, translateStory(err)
	}

	out := make([]story.Mine, 0, len(rows))
	for _, row := range rows {
		duration := 0
		if row.DurationMs != nil {
			duration = *row.DurationMs
		}
		out = append(out, story.Mine{
			ID:         row.ID,
			MediaID:    row.MediaID,
			MediaKind:  row.MediaKind,
			StorageKey: row.StorageKey,
			DurationMs: duration,
			ViewCount:  row.ViewCount,
			CreatedAt:  row.CreatedAt,
			ExpiresAt:  row.ExpiresAt,
			IsExpired:  row.IsExpired,
		})
	}
	return out, nil
}

// Delete memanggil fungsi delete_story.
func (r *StoryRepository) Delete(ctx context.Context, storyID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}

	if err := r.client.RPC(ctx, "delete_story",
		map[string]any{"p_story": storyID}, nil, actor); err != nil {
		return translateStory(err)
	}
	return nil
}

// MarkViewed memanggil fungsi mark_story_viewed.
func (r *StoryRepository) MarkViewed(ctx context.Context, storyID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}

	if err := r.client.RPC(ctx, "mark_story_viewed",
		map[string]any{"p_story": storyID}, nil, actor); err != nil {
		return translateStory(err)
	}
	return nil
}

func translateStory(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		// Dipakai dua fungsi: create_story menolak media orang lain,
		// delete_story menolak story orang lain. Keduanya sama-sama 403 di
		// lapisan transport, jadi pembedaannya tidak mengubah apa pun.
		return story.ErrNotOwner
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return story.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return story.ErrInvalidInput
	default:
		return err
	}
}
