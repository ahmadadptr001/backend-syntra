package supabase

import (
	"context"
	"errors"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/account"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// Helper kecil untuk mengubah pointer domain menjadi argumen RPC. nil menjadi
// NULL, yang di fungsi database berarti "biarkan seperti semula".
func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolPtr(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

func newUUID() string { return id.New() }

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// ProfileRepository memenuhi kontrak account.ProfileStore dan account.UserFinder.
type ProfileRepository struct {
	client *sb.Client
}

// NewProfileRepository membuat repository profil.
func NewProfileRepository(client *sb.Client) *ProfileRepository {
	return &ProfileRepository{client: client}
}

var (
	_ account.ProfileStore = (*ProfileRepository)(nil)
	_ account.UserFinder   = (*ProfileRepository)(nil)
)

type myProfileRow struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	Email           string  `json:"email"`
	DisplayName     string  `json:"display_name"`
	Bio             string  `json:"bio"`
	AvatarKey       string  `json:"avatar_key"`
	CoverKey        string  `json:"cover_key"`
	FollowerCount   int     `json:"follower_count"`
	FollowingCount  int     `json:"following_count"`
	IsPrivate       bool    `json:"is_private"`
	DateOfBirth     *string `json:"date_of_birth"`
	DMPrivacy       string  `json:"dm_privacy"`
	StoryPrivacy    string  `json:"story_privacy"`
	PresenceVisible bool    `json:"presence_visible"`
	Locale          string  `json:"locale"`
}

// GetMyProfile memanggil fungsi get_my_profile.
func (r *ProfileRepository) GetMyProfile(ctx context.Context) (account.MyProfile, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return account.MyProfile{}, err
	}

	var rows []myProfileRow
	if err := r.client.RPC(ctx, "get_my_profile", map[string]any{}, &rows, actor); err != nil {
		return account.MyProfile{}, translateProfile(err)
	}
	if len(rows) == 0 {
		return account.MyProfile{}, account.ErrProfileNotFound
	}

	row := rows[0]
	return account.MyProfile{
		ID:              row.ID,
		Username:        row.Username,
		Email:           row.Email,
		DisplayName:     row.DisplayName,
		Bio:             row.Bio,
		AvatarKey:       row.AvatarKey,
		CoverKey:        row.CoverKey,
		FollowerCount:   row.FollowerCount,
		FollowingCount:  row.FollowingCount,
		IsPrivate:       row.IsPrivate,
		DateOfBirth:     deref(row.DateOfBirth),
		DMPrivacy:       row.DMPrivacy,
		StoryPrivacy:    row.StoryPrivacy,
		PresenceVisible: row.PresenceVisible,
		Locale:          row.Locale,
	}, nil
}

// UpdateMyProfile memanggil fungsi update_my_profile.
//
// Field nil dikirim sebagai NULL, yang di fungsi berarti "biarkan seperti
// semula" — jadi klien bisa mengirim hanya yang benar-benar berubah.
// ClearCover memanggil clear_profile_cover — mengosongkan cover_media_id.
func (r *ProfileRepository) ClearCover(ctx context.Context) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "clear_profile_cover", map[string]any{}, nil, actor); err != nil {
		return translateProfile(err)
	}
	return nil
}

func (r *ProfileRepository) UpdateMyProfile(ctx context.Context, in account.UpdateProfileInput) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_display_name":  strPtr(in.DisplayName),
		"p_bio":           strPtr(in.Bio),
		"p_avatar_media":  strPtr(in.AvatarMediaID),
		"p_is_private":    boolPtr(in.IsPrivate),
		"p_dm_privacy":    strPtr(in.DMPrivacy),
		"p_story_privacy": strPtr(in.StoryPrivacy),
	}
	// p_username & p_cover_media hanya disertakan saat benar-benar diisi. Tanpa
	// ini, tiap edit profil mengirim 8 argumen dan hanya cocok dengan versi
	// fungsi dari migrasi 17 — sehingga edit profil biasa (nama/bio) akan 404
	// sebelum migrasi itu dijalankan. Keduanya fitur baru yang memang butuh
	// migrasi tersebut.
	if in.Username != nil {
		args["p_username"] = *in.Username
	}
	if in.CoverMediaID != nil {
		args["p_cover_media"] = *in.CoverMediaID
	}
	// p_presence_visible juga hanya disertakan saat diisi — sama alasannya:
	// mengirimnya selalu akan menuntut versi fungsi dari migrasi 26.
	if in.PresenceVisible != nil {
		args["p_presence_visible"] = *in.PresenceVisible
	}

	if err := r.client.RPC(ctx, "update_my_profile", args, nil, actor); err != nil {
		// Pelanggaran keunikan pada jalur ini hanya bisa berasal dari username —
		// dipetakan di sini, bukan di translateProfile yang dipakai bersama RPC
		// lain (create_report, register_device) yang tak boleh ikut membalas
		// "username sudah dipakai".
		var apiErr *sb.APIError
		if errorsAs(err, &apiErr) && apiErr.Code == sqlstateUniqueViolation {
			return account.ErrUsernameTaken
		}
		return translateProfile(err)
	}
	return nil
}

// FindID menukar username menjadi id lewat fungsi find_user.
func (r *ProfileRepository) FindID(ctx context.Context, username string) (string, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return "", err
	}

	var rows []struct {
		ID string `json:"id"`
	}
	if err := r.client.RPC(ctx, "find_user",
		map[string]any{"p_username": username}, &rows, actor); err != nil {
		return "", translateProfile(err)
	}
	if len(rows) == 0 {
		return "", account.ErrProfileNotFound
	}
	return rows[0].ID, nil
}

// BlockUser memanggil fungsi block_user.
func (r *ProfileRepository) BlockUser(ctx context.Context, targetID string) error {
	return r.simpleRPC(ctx, "block_user", map[string]any{"p_target": targetID})
}

// UnblockUser memanggil fungsi unblock_user.
func (r *ProfileRepository) UnblockUser(ctx context.Context, targetID string) error {
	return r.simpleRPC(ctx, "unblock_user", map[string]any{"p_target": targetID})
}

type blockedRow struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarKey   string `json:"avatar_key"`
}

// ListBlocked memanggil fungsi list_blocked.
func (r *ProfileRepository) ListBlocked(ctx context.Context) ([]account.BlockedUser, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []blockedRow
	if err := r.client.RPC(ctx, "list_blocked", map[string]any{}, &rows, actor); err != nil {
		return nil, translateProfile(err)
	}

	out := make([]account.BlockedUser, 0, len(rows))
	for _, row := range rows {
		out = append(out, account.BlockedUser{
			UserID:      row.UserID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			AvatarKey:   row.AvatarKey,
		})
	}
	return out, nil
}

// RegisterDevice memanggil fungsi register_device.
func (r *ProfileRepository) RegisterDevice(ctx context.Context, deviceID, platform, pushToken, appVersion string) error {
	return r.simpleRPC(ctx, "register_device", map[string]any{
		"p_id":          deviceID,
		"p_platform":    platform,
		"p_push_token":  pushToken,
		"p_app_version": nullIfEmpty(appVersion),
	})
}

// RevokeDevice memanggil fungsi revoke_device.
func (r *ProfileRepository) RevokeDevice(ctx context.Context, deviceID string) error {
	return r.simpleRPC(ctx, "revoke_device", map[string]any{"p_id": deviceID})
}

// CreateReport memanggil fungsi create_report.
func (r *ProfileRepository) CreateReport(ctx context.Context, targetType, targetID, reason, detail string) error {
	return r.simpleRPC(ctx, "create_report", map[string]any{
		"p_id":          newUUID(),
		"p_target_type": targetType,
		"p_target":      targetID,
		"p_reason":      reason,
		"p_detail":      nullIfEmpty(detail),
	})
}

func (r *ProfileRepository) simpleRPC(ctx context.Context, fn string, args map[string]any) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, fn, args, nil, actor); err != nil {
		return translateProfile(err)
	}
	return nil
}

func translateProfile(err error) error {
	var apiErr *sb.APIError
	if !errorsAs(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateInvalidData:
		return account.ErrInvalidInput
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return account.ErrProfileNotFound
	default:
		return err
	}
}
