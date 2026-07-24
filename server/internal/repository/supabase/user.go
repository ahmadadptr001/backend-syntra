package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/user"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// UserRepository memenuhi kontrak user.Repository.
type UserRepository struct {
	client *sb.Client
}

// NewUserRepository membuat repository direktori pengguna.
func NewUserRepository(client *sb.Client) *UserRepository {
	return &UserRepository{client: client}
}

var _ user.Repository = (*UserRepository)(nil)

type profileRow struct {
	ID             string  `json:"id"`
	Username       string  `json:"username"`
	DisplayName    string  `json:"display_name"`
	AvatarMediaID  *string `json:"avatar_media_id"`
	FollowerCount  int     `json:"follower_count"`
	FollowingCount int     `json:"following_count"`
	FollowStatus   string  `json:"follow_status"`
	IsSelf         bool    `json:"is_self"`
}

type followingRow struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	AvatarMediaID *string   `json:"avatar_media_id"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// FindByUsername memanggil fungsi find_user.
//
// Berbeda dari repository lain, di sini tidak ada userID yang bisa
// dicocokkan — pencarian dilakukan atas nama siapa pun yang sedang login,
// jadi token diambil langsung dari konteks.
func (r *UserRepository) FindByUsername(ctx context.Context, username string) (user.Profile, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return user.Profile{}, err
	}

	var rows []profileRow
	if err := r.client.RPC(ctx, "find_user",
		map[string]any{"p_username": username}, &rows, actor); err != nil {
		return user.Profile{}, translateUser(err)
	}

	// Fungsi memakai LIMIT 1, jadi hasil kosong berarti benar-benar tidak ada.
	if len(rows) == 0 {
		return user.Profile{}, user.ErrNotFound
	}

	row := rows[0]
	return user.Profile{
		ID:             row.ID,
		Username:       row.Username,
		DisplayName:    row.DisplayName,
		AvatarMediaID:  deref(row.AvatarMediaID),
		FollowerCount:  row.FollowerCount,
		FollowingCount: row.FollowingCount,
		FollowStatus:   user.FollowStatus(row.FollowStatus),
		IsSelf:         row.IsSelf,
	}, nil
}

// Follow memanggil fungsi follow_user.
func (r *UserRepository) Follow(ctx context.Context, targetID string) (user.FollowStatus, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return user.FollowNone, err
	}

	var status string
	if err := r.client.RPC(ctx, "follow_user",
		map[string]any{"p_target": targetID}, &status, actor); err != nil {
		return user.FollowNone, translateUser(err)
	}
	return user.FollowStatus(status), nil
}

// Unfollow memanggil fungsi unfollow_user.
func (r *UserRepository) Unfollow(ctx context.Context, targetID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	if err := r.client.RPC(ctx, "unfollow_user",
		map[string]any{"p_target": targetID}, nil, actor); err != nil {
		return translateUser(err)
	}
	return nil
}

// ListFollowing memanggil fungsi list_following.
func (r *UserRepository) ListFollowing(ctx context.Context) ([]user.Profile, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []followingRow
	if err := r.client.RPC(ctx, "list_following", map[string]any{}, &rows, actor); err != nil {
		return nil, translateUser(err)
	}

	out := make([]user.Profile, 0, len(rows))
	for _, row := range rows {
		out = append(out, user.Profile{
			ID:            row.ID,
			Username:      row.Username,
			DisplayName:   row.DisplayName,
			AvatarMediaID: deref(row.AvatarMediaID),
			FollowStatus:  user.FollowStatus(row.Status),
			FollowedAt:    row.CreatedAt,
		})
	}
	return out, nil
}

// ListFollowers memanggil fungsi list_followers.
//
// username kosong dibiarkan tanpa argumen supaya fungsi memakai DEFAULT NULL,
// yang berarti pengikut pemanggil sendiri.
func (r *UserRepository) ListFollowers(ctx context.Context, username string) ([]user.Profile, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	args := map[string]any{}
	if username != "" {
		args["p_username"] = username
	}

	var rows []followingRow
	if err := r.client.RPC(ctx, "list_followers", args, &rows, actor); err != nil {
		return nil, translateUser(err)
	}

	out := make([]user.Profile, 0, len(rows))
	for _, row := range rows {
		out = append(out, user.Profile{
			ID:            row.ID,
			Username:      row.Username,
			DisplayName:   row.DisplayName,
			AvatarMediaID: deref(row.AvatarMediaID),
			FollowStatus:  user.FollowStatus(row.Status),
			FollowedAt:    row.CreatedAt,
		})
	}
	return out, nil
}

// ListFollowRequests memanggil fungsi list_follow_requests.
func (r *UserRepository) ListFollowRequests(ctx context.Context) ([]user.Profile, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []followingRow
	if err := r.client.RPC(ctx, "list_follow_requests", map[string]any{}, &rows, actor); err != nil {
		return nil, translateUser(err)
	}

	out := make([]user.Profile, 0, len(rows))
	for _, row := range rows {
		out = append(out, user.Profile{
			ID:            row.ID,
			Username:      row.Username,
			DisplayName:   row.DisplayName,
			AvatarMediaID: deref(row.AvatarMediaID),
			FollowStatus:  user.FollowPending,
			FollowedAt:    row.CreatedAt,
		})
	}
	return out, nil
}

// DecideFollowRequest memanggil fungsi decide_follow_request.
func (r *UserRepository) DecideFollowRequest(ctx context.Context, followerID string, approve bool) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{"p_follower": followerID, "p_approve": approve}
	if err := r.client.RPC(ctx, "decide_follow_request", args, nil, actor); err != nil {
		return translateUser(err)
	}
	return nil
}

// callerOption mengambil JWT pemanggil tanpa memeriksa kecocokan id.
//
// Dipakai operasi yang memang bertindak atas nama siapa pun yang sedang login,
// bukan atas nama pengguna tertentu yang disebut pemanggil.
func callerOption(ctx context.Context) (sb.Option, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.Token == "" {
		return nil, ErrMissingToken
	}
	return sb.WithToken(principal.Token), nil
}

func translateUser(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return user.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return user.ErrInvalidInput
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return user.ErrNotAllowed
	default:
		return err
	}
}
