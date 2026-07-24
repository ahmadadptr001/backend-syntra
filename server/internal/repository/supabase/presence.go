package supabase

import (
	"context"
	"net/url"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/presence"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// PresenceVisibilityRepository membaca setelan presence_visible seorang
// pengguna. Memenuhi port presence.Visibility.
//
// Dipakai saat koneksi WebSocket terbentuk — pemanggil membaca setelannya
// sendiri, jadi RLS user_settings_self (user_id = auth.uid()) sudah cukup dan
// tidak butuh kunci service.
type PresenceVisibilityRepository struct {
	client *sb.Client
}

// NewPresenceVisibilityRepository membuat repository visibilitas presence.
func NewPresenceVisibilityRepository(client *sb.Client) *PresenceVisibilityRepository {
	return &PresenceVisibilityRepository{client: client}
}

var _ presence.Visibility = (*PresenceVisibilityRepository)(nil)

type presenceVisibleRow struct {
	PresenceVisible bool `json:"presence_visible"`
}

// IsVisible mengembalikan apakah pengguna mengizinkan status online-nya
// terlihat. Baris tidak ada (mis. setelan belum terbentuk) dianggap terlihat.
func (r *PresenceVisibilityRepository) IsVisible(ctx context.Context, userID string) (bool, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return true, err
	}

	q := url.Values{}
	q.Set("select", "presence_visible")
	q.Set("user_id", "eq."+userID)
	q.Set("limit", "1")

	var rows []presenceVisibleRow
	if err := r.client.Select(ctx, "user_settings", &rows, actor, sb.WithQuery(q)); err != nil {
		return true, err
	}
	if len(rows) == 0 {
		return true, nil
	}
	return rows[0].PresenceVisible, nil
}
