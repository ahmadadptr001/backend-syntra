package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/live"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// LiveRepository memenuhi kontrak live.Repository.
type LiveRepository struct {
	client *sb.Client
}

// NewLiveRepository membuat repository live.
func NewLiveRepository(client *sb.Client) *LiveRepository {
	return &LiveRepository{client: client}
}

var _ live.Repository = (*LiveRepository)(nil)

type liveRow struct {
	ID           string    `json:"id"`
	HostID       string    `json:"host_id"`
	HostUsername string    `json:"host_username"`
	HostName     string    `json:"host_name"`
	HostAvatar   *string   `json:"host_avatar"`
	Title        string    `json:"title"`
	Category     string    `json:"category"`
	ViewerCount  int       `json:"viewer_count"`
	StartedAt    time.Time `json:"started_at"`
}

type liveJoinRow struct {
	Role      string  `json:"role"`
	SFURoomID *string `json:"sfu_room_id"`
}

func (r liveRow) toDomain() live.Live {
	return live.Live{
		ID:           r.ID,
		HostID:       r.HostID,
		HostUsername: r.HostUsername,
		HostName:     r.HostName,
		HostAvatarID: deref(r.HostAvatar),
		Title:        r.Title,
		Category:     r.Category,
		ViewerCount:  r.ViewerCount,
		StartedAt:    r.StartedAt,
	}
}

// Create memanggil fungsi create_live.
func (r *LiveRepository) Create(ctx context.Context, l live.Live) error {
	actor, err := actorOption(ctx, l.HostID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_id":       l.ID,
		"p_title":    l.Title,
		"p_category": l.Category,
		"p_sfu_room": l.SFURoomID,
	}
	if err := r.client.RPC(ctx, "create_live", args, nil, actor); err != nil {
		return translateLive(err)
	}
	return nil
}

// List memanggil fungsi list_lives.
func (r *LiveRepository) List(ctx context.Context) ([]live.Live, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []liveRow
	if err := r.client.RPC(ctx, "list_lives", map[string]any{}, &rows, actor); err != nil {
		return nil, translateLive(err)
	}

	out := make([]live.Live, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// Get memanggil fungsi get_live. Baris kosong berarti live sudah berakhir.
func (r *LiveRepository) Get(ctx context.Context, liveID string) (live.Live, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return live.Live{}, err
	}

	var rows []liveRow
	if err := r.client.RPC(ctx, "get_live",
		map[string]any{"p_live": liveID}, &rows, actor); err != nil {
		return live.Live{}, translateLive(err)
	}
	if len(rows) == 0 {
		return live.Live{}, live.ErrNotFound
	}
	return rows[0].toDomain(), nil
}

// Join memanggil fungsi join_live.
func (r *LiveRepository) Join(ctx context.Context, liveID string) (live.Role, string, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return "", "", err
	}

	var rows []liveJoinRow
	if err := r.client.RPC(ctx, "join_live",
		map[string]any{"p_live": liveID}, &rows, actor); err != nil {
		return "", "", translateLive(err)
	}
	if len(rows) == 0 {
		return "", "", live.ErrNotFound
	}
	return live.Role(rows[0].Role), deref(rows[0].SFURoomID), nil
}

// End memanggil fungsi end_live_by_host.
func (r *LiveRepository) End(ctx context.Context, liveID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "end_live_by_host",
		map[string]any{"p_live": liveID}, nil, actor); err != nil {
		return translateLive(err)
	}
	return nil
}

// Leave memanggil fungsi leave_live. Mengembalikan true kalau yang keluar host.
func (r *LiveRepository) Leave(ctx context.Context, liveID string) (bool, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return false, err
	}

	var ended bool
	if err := r.client.RPC(ctx, "leave_live",
		map[string]any{"p_live": liveID}, &ended, actor); err != nil {
		return false, translateLive(err)
	}
	return ended, nil
}

// CloseStale memanggil fungsi close_stale_lives dengan kunci service — ini
// pekerjaan latar yang tidak mewakili pengguna mana pun.
func (r *LiveRepository) CloseStale(ctx context.Context, idleMinutes int) (int, error) {
	var closed int
	if err := r.client.RPC(ctx, "close_stale_lives",
		map[string]any{"p_idle_minutes": idleMinutes}, &closed,
		sb.WithServiceRole()); err != nil {
		return 0, translateLive(err)
	}
	return closed, nil
}

// sqlstateInsufficientCoin ditegakkan send_live_gift saat saldo koin kurang.
const sqlstateInsufficientCoin = "P0003"

type giftRow struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Emoji string `json:"emoji"`
	Name  string `json:"name"`
	Cost  int    `json:"cost"`
}

type giftResultRow struct {
	Emoji          string `json:"emoji"`
	Name           string `json:"name"`
	Cost           int    `json:"cost"`
	Balance        int    `json:"balance"`
	SenderUsername string `json:"sender_username"`
}

// GetWallet memanggil fungsi get_wallet.
func (r *LiveRepository) GetWallet(ctx context.Context) (int, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return 0, err
	}
	var balance int
	if err := r.client.RPC(ctx, "get_wallet", map[string]any{}, &balance, actor); err != nil {
		return 0, translateLive(err)
	}
	return balance, nil
}

// TopUp memanggil fungsi topup_wallet.
func (r *LiveRepository) TopUp(ctx context.Context, amount int) (int, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return 0, err
	}
	var balance int
	if err := r.client.RPC(ctx, "topup_wallet",
		map[string]any{"p_amount": amount}, &balance, actor); err != nil {
		return 0, translateLive(err)
	}
	return balance, nil
}

// ListGifts memanggil fungsi list_gifts.
func (r *LiveRepository) ListGifts(ctx context.Context) ([]live.Gift, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}
	var rows []giftRow
	if err := r.client.RPC(ctx, "list_gifts", map[string]any{}, &rows, actor); err != nil {
		return nil, translateLive(err)
	}
	out := make([]live.Gift, 0, len(rows))
	for _, row := range rows {
		out = append(out, live.Gift{
			ID:    row.ID,
			Code:  row.Code,
			Emoji: row.Emoji,
			Name:  row.Name,
			Cost:  row.Cost,
		})
	}
	return out, nil
}

// SendGift memanggil fungsi send_live_gift (pengurangan koin atomik di DB).
func (r *LiveRepository) SendGift(ctx context.Context, giftRowID, liveID, giftID string) (live.GiftResult, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return live.GiftResult{}, err
	}
	var rows []giftResultRow
	if err := r.client.RPC(ctx, "send_live_gift",
		map[string]any{"p_id": giftRowID, "p_live": liveID, "p_gift": giftID}, &rows, actor); err != nil {
		return live.GiftResult{}, translateLive(err)
	}
	if len(rows) == 0 {
		return live.GiftResult{}, live.ErrNotFound
	}
	return live.GiftResult{
		Emoji:          rows[0].Emoji,
		Name:           rows[0].Name,
		Cost:           rows[0].Cost,
		Balance:        rows[0].Balance,
		SenderUsername: rows[0].SenderUsername,
	}, nil
}

func translateLive(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateInsufficientCoin:
		return live.ErrInsufficientCoin
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return live.ErrNotAllowed
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return live.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return live.ErrInvalidInput
	default:
		return err
	}
}
