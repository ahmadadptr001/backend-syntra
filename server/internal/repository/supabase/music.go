package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/music"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// MusicRepository memenuhi kontrak music.Repository.
type MusicRepository struct {
	client *sb.Client
}

// NewMusicRepository membuat repository musik.
func NewMusicRepository(client *sb.Client) *MusicRepository {
	return &MusicRepository{client: client}
}

var _ music.Repository = (*MusicRepository)(nil)

// musicRow memetakan kolom yang dikembalikan fungsi-fungsi feed/search musik.
type musicRow struct {
	ID              string    `json:"id"`
	AuthorID        string    `json:"author_id"`
	AuthorUsername  string    `json:"author_username"`
	AuthorName      string    `json:"author_name"`
	StorageKey      string    `json:"storage_key"`
	CoverStorageKey *string   `json:"cover_storage_key"`
	Title           string    `json:"title"`
	Artist          string    `json:"artist"`
	DurationMs      *int      `json:"duration_ms"`
	CreatedAt       time.Time `json:"created_at"`
}

func (row musicRow) toDomain() music.Track {
	dur := 0
	if row.DurationMs != nil {
		dur = *row.DurationMs
	}
	return music.Track{
		ID:              row.ID,
		AuthorID:        row.AuthorID,
		AuthorUsername:  row.AuthorUsername,
		AuthorName:      row.AuthorName,
		StorageKey:      row.StorageKey,
		CoverStorageKey: deref(row.CoverStorageKey),
		Title:           row.Title,
		Artist:          row.Artist,
		DurationMs:      dur,
		CreatedAt:       row.CreatedAt,
	}
}

// Create memanggil create_music_track.
func (r *MusicRepository) Create(ctx context.Context, t music.Track) error {
	actor, err := actorOption(ctx, t.AuthorID)
	if err != nil {
		return err
	}
	args := map[string]any{
		"p_id":          t.ID,
		"p_media":       t.MediaID,
		"p_cover":       nullIfEmpty(t.CoverMediaID),
		"p_title":       t.Title,
		"p_artist":      t.Artist,
		"p_duration_ms": t.DurationMs,
	}
	if err := r.client.RPC(ctx, "create_music_track", args, nil, actor); err != nil {
		return translateMusic(err)
	}
	return nil
}

// Feed memanggil list_music_feed.
func (r *MusicRepository) Feed(ctx context.Context, userID string, limit int) ([]music.Track, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	var rows []musicRow
	if err := r.client.RPC(ctx, "list_music_feed", map[string]any{"p_limit": limit}, &rows, actor); err != nil {
		return nil, translateMusic(err)
	}
	return musicToDomain(rows), nil
}

// Search memanggil search_music_tracks.
func (r *MusicRepository) Search(ctx context.Context, userID, query string, limit int) ([]music.Track, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	var rows []musicRow
	if err := r.client.RPC(ctx, "search_music_tracks",
		map[string]any{"p_q": query, "p_limit": limit}, &rows, actor); err != nil {
		return nil, translateMusic(err)
	}
	return musicToDomain(rows), nil
}

// Delete memanggil delete_music_track.
func (r *MusicRepository) Delete(ctx context.Context, trackID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "delete_music_track", map[string]any{"p_id": trackID}, nil, actor); err != nil {
		return translateMusic(err)
	}
	return nil
}

// UpdateTitle memanggil update_music_track_title (pemilik saja).
func (r *MusicRepository) UpdateTitle(ctx context.Context, trackID, userID, title string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	args := map[string]any{"p_id": trackID, "p_title": title}
	if err := r.client.RPC(ctx, "update_music_track_title", args, nil, actor); err != nil {
		return translateMusic(err)
	}
	return nil
}

func musicToDomain(rows []musicRow) []music.Track {
	out := make([]music.Track, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out
}

func translateMusic(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return music.ErrNotAllowed
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return music.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return music.ErrInvalidInput
	default:
		return err
	}
}
