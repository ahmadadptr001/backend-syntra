package supabase

import (
	"context"
	"errors"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/media"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// MediaRepository memenuhi kontrak media.Repository.
type MediaRepository struct {
	client *sb.Client
}

// NewMediaRepository membuat repository media.
func NewMediaRepository(client *sb.Client) *MediaRepository {
	return &MediaRepository{client: client}
}

var _ media.Repository = (*MediaRepository)(nil)

// Register memanggil fungsi register_media.
func (r *MediaRepository) Register(ctx context.Context, a media.Asset) error {
	actor, err := actorOption(ctx, a.OwnerID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_id":          a.ID,
		"p_kind":        string(a.Kind),
		"p_storage_key": a.StorageKey,
		"p_mime":        a.MimeType,
		"p_size":        a.SizeBytes,
		"p_duration_ms": nullIfZero(a.DurationMs),
		"p_width":       nullIfZero(a.Width),
		"p_height":      nullIfZero(a.Height),
	}

	if err := r.client.RPC(ctx, "register_media", args, nil, actor); err != nil {
		return translateMedia(err)
	}
	return nil
}

// Delete memanggil fungsi delete_media.
//
// Fungsi itu menegakkan kepemilikan dan menolak media yang masih ditunjuk
// sesuatu; di sini tinggal menerjemahkan galatnya dan membaca storage_key yang
// dikembalikan supaya berkasnya bisa ikut dibuang.
func (r *MediaRepository) Delete(ctx context.Context, mediaID, ownerID string) (string, error) {
	actor, err := actorOption(ctx, ownerID)
	if err != nil {
		return "", err
	}

	var rows []struct {
		StorageKey string `json:"storage_key"`
	}
	if err := r.client.RPC(ctx, "delete_media",
		map[string]any{"p_id": mediaID}, &rows, actor); err != nil {
		return "", translateMedia(err)
	}

	if len(rows) == 0 {
		return "", media.ErrNotFound
	}
	return rows[0].StorageKey, nil
}

// MediaStorage memenuhi kontrak media.Storage.
//
// Terpisah dari MediaRepository karena keduanya memang berbicara ke sistem
// yang berbeda: yang satu ke PostgREST, yang satu ke Supabase Storage.
type MediaStorage struct {
	client *sb.Client
}

// NewMediaStorage membuat adaptor object storage.
func NewMediaStorage(client *sb.Client) *MediaStorage {
	return &MediaStorage{client: client}
}

var _ media.Storage = (*MediaStorage)(nil)

// SignUpload meminta izin unggah atas nama pengguna yang sedang login.
func (s *MediaStorage) SignUpload(ctx context.Context, bucket, path string) (string, string, error) {
	actor, err := actorOption(ctx, "")
	if err != nil {
		return "", "", err
	}

	signed, err := s.client.CreateSignedUploadURL(ctx, bucket, path, actor)
	if err != nil {
		return "", "", translateMedia(err)
	}
	return signed.URL, signed.Token, nil
}

// PublicURL menyusun URL baca objek.
func (s *MediaStorage) PublicURL(bucket, path string) string {
	return s.client.PublicObjectURL(bucket, path)
}

// Delete membuang satu objek atas nama pengguna yang sedang login.
func (s *MediaStorage) Delete(ctx context.Context, bucket, path string) error {
	actor, err := actorOption(ctx, "")
	if err != nil {
		return err
	}

	if err := s.client.DeleteObject(ctx, bucket, path, actor); err != nil {
		return translateMedia(err)
	}
	return nil
}

func translateMedia(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateInUse:
		return media.ErrInUse
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return media.ErrNotOwner
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return media.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return media.ErrInvalidInput
	default:
		return err
	}
}

// nullIfZero mengubah 0 menjadi NULL. Dimensi dan durasi bernilai nol berarti
// "tidak diketahui" (misalnya untuk gambar), bukan benar-benar nol.
func nullIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
