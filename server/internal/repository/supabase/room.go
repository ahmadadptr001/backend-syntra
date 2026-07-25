package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/room"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// SQLSTATE tambahan yang dipakai fungsi room.
const sqlstateRoomFull = "53400" // configuration_limit_exceeded

// RoomRepository memenuhi kontrak room.Repository.
type RoomRepository struct {
	client *sb.Client
}

// NewRoomRepository membuat repository voice room.
func NewRoomRepository(client *sb.Client) *RoomRepository {
	return &RoomRepository{client: client}
}

var _ room.Repository = (*RoomRepository)(nil)

type roomRow struct {
	ID               string    `json:"id"`
	HostID           string    `json:"host_id"`
	HostUsername     string    `json:"host_username"`
	HostName         string    `json:"host_name"`
	HostAvatar       *string   `json:"host_avatar"`
	HostCover        *string   `json:"host_cover"`
	Title            string    `json:"title"`
	Topic            string    `json:"topic"`
	Visibility       string    `json:"visibility"`
	ParticipantCount int       `json:"participant_count"`
	SpeakerCount     int       `json:"speaker_count"`
	MaxParticipants  int       `json:"max_participants"`
	StartedAt        time.Time `json:"started_at"`
}

type joinRow struct {
	Status    string  `json:"status"`
	Role      string  `json:"role"`
	SFURoomID *string `json:"sfu_room_id"`
}

type participantRow struct {
	UserID        string    `json:"user_id"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	AvatarKey     string    `json:"avatar_key"`
	CoverKey      string    `json:"cover_key"`
	Role          string    `json:"role"`
	IsMuted       bool      `json:"is_muted"`
	HasRaisedHand bool      `json:"has_raised_hand"`
	JoinedAt      time.Time `json:"joined_at"`
}

type speakRequestRow struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarKey   string    `json:"avatar_key"`
	RequestedAt time.Time `json:"requested_at"`
}

// Create memanggil fungsi create_room.
func (r *RoomRepository) Create(ctx context.Context, rm room.Room) error {
	actor, err := actorOption(ctx, rm.HostID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_id":         rm.ID,
		"p_title":      rm.Title,
		"p_topic":      rm.Topic,
		"p_visibility": string(rm.Visibility),
		"p_sfu_room":   rm.SFURoomID,
	}

	if err := r.client.RPC(ctx, "create_room", args, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// List memanggil fungsi list_rooms.
func (r *RoomRepository) List(ctx context.Context) ([]room.Room, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []roomRow
	if err := r.client.RPC(ctx, "list_rooms", map[string]any{}, &rows, actor); err != nil {
		return nil, translateRoom(err)
	}

	out := make([]room.Room, 0, len(rows))
	for _, row := range rows {
		out = append(out, room.Room{
			ID:               row.ID,
			HostID:           row.HostID,
			HostUsername:     row.HostUsername,
			HostName:         row.HostName,
			HostAvatarID:     deref(row.HostAvatar),
			HostCoverID:      deref(row.HostCover),
			Title:            row.Title,
			Topic:            row.Topic,
			Visibility:       room.Visibility(row.Visibility),
			ParticipantCount: row.ParticipantCount,
			SpeakerCount:     row.SpeakerCount,
			MaxParticipants:  row.MaxParticipants,
			StartedAt:        row.StartedAt,
		})
	}
	return out, nil
}

// Join memanggil fungsi join_room.
//
// Mengembalikan status "pending" untuk room invite_only yang permintaannya
// masih menunggu keputusan host.
func (r *RoomRepository) Join(ctx context.Context, roomID string) (room.JoinStatus, room.Role, string, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return "", "", "", err
	}

	var rows []joinRow
	if err := r.client.RPC(ctx, "join_room",
		map[string]any{"p_room": roomID}, &rows, actor); err != nil {
		return "", "", "", translateRoom(err)
	}
	if len(rows) == 0 {
		return "", "", "", room.ErrNotFound
	}

	return room.JoinStatus(rows[0].Status), room.Role(rows[0].Role), deref(rows[0].SFURoomID), nil
}

// End memanggil fungsi end_room_by_host.
func (r *RoomRepository) End(ctx context.Context, roomID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "end_room_by_host",
		map[string]any{"p_room": roomID}, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// CancelSpeakRequest memanggil fungsi cancel_speak_request.
func (r *RoomRepository) CancelSpeakRequest(ctx context.Context, roomID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "cancel_speak_request",
		map[string]any{"p_room": roomID}, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// ListJoinRequests memanggil fungsi list_join_requests.
func (r *RoomRepository) ListJoinRequests(ctx context.Context, roomID string) ([]room.SpeakRequest, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []speakRequestRow
	if err := r.client.RPC(ctx, "list_join_requests",
		map[string]any{"p_room": roomID}, &rows, actor); err != nil {
		return nil, translateRoom(err)
	}

	out := make([]room.SpeakRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, room.SpeakRequest{
			UserID:      row.UserID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			AvatarKey:   row.AvatarKey,
			RequestedAt: row.RequestedAt,
		})
	}
	return out, nil
}

// DecideJoinRequest memanggil fungsi decide_join_request.
func (r *RoomRepository) DecideJoinRequest(ctx context.Context, roomID, userID string, approve bool) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{"p_room": roomID, "p_user": userID, "p_approve": approve}
	if err := r.client.RPC(ctx, "decide_join_request", args, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// Leave memanggil fungsi leave_room.
//
// Mengembalikan true kalau yang keluar adalah host — artinya room ikut
// berakhir dan peserta lain perlu diberi tahu.
func (r *RoomRepository) Leave(ctx context.Context, roomID string) (bool, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return false, err
	}

	var ended bool
	if err := r.client.RPC(ctx, "leave_room",
		map[string]any{"p_room": roomID}, &ended, actor); err != nil {
		return false, translateRoom(err)
	}
	return ended, nil
}

// ListSpeakRequests memanggil fungsi list_speak_requests.
func (r *RoomRepository) ListSpeakRequests(ctx context.Context, roomID string) ([]room.SpeakRequest, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []speakRequestRow
	if err := r.client.RPC(ctx, "list_speak_requests",
		map[string]any{"p_room": roomID}, &rows, actor); err != nil {
		return nil, translateRoom(err)
	}

	out := make([]room.SpeakRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, room.SpeakRequest{
			UserID:      row.UserID,
			Username:    row.Username,
			DisplayName: row.DisplayName,
			AvatarKey:   row.AvatarKey,
			RequestedAt: row.RequestedAt,
		})
	}
	return out, nil
}

// Invite memanggil fungsi invite_to_room.
func (r *RoomRepository) Invite(ctx context.Context, roomID, userID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{"p_room": roomID, "p_user": userID}
	if err := r.client.RPC(ctx, "invite_to_room", args, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// CloseStale memanggil fungsi close_stale_rooms.
//
// Memakai kunci service karena ini pekerjaan latar yang tidak mewakili
// pengguna mana pun — tidak ada JWT yang bisa dipinjam.
func (r *RoomRepository) CloseStale(ctx context.Context, idleMinutes int) (int, error) {
	var closed int
	if err := r.client.RPC(ctx, "close_stale_rooms",
		map[string]any{"p_idle_minutes": idleMinutes}, &closed,
		sb.WithServiceRole()); err != nil {
		return 0, translateRoom(err)
	}
	return closed, nil
}

// ListParticipants memanggil fungsi list_room_participants.
func (r *RoomRepository) ListParticipants(ctx context.Context, roomID string) ([]room.Participant, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	var rows []participantRow
	if err := r.client.RPC(ctx, "list_room_participants",
		map[string]any{"p_room": roomID}, &rows, actor); err != nil {
		return nil, translateRoom(err)
	}

	out := make([]room.Participant, 0, len(rows))
	for _, row := range rows {
		out = append(out, room.Participant{
			UserID:        row.UserID,
			Username:      row.Username,
			DisplayName:   row.DisplayName,
			AvatarKey:     row.AvatarKey,
			CoverKey:      row.CoverKey,
			Role:          room.Role(row.Role),
			IsMuted:       row.IsMuted,
			HasRaisedHand: row.HasRaisedHand,
			JoinedAt:      row.JoinedAt,
		})
	}
	return out, nil
}

// SetRole memanggil fungsi set_room_role.
func (r *RoomRepository) SetRole(ctx context.Context, roomID, targetID string, role room.Role) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_room":   roomID,
		"p_target": targetID,
		"p_role":   string(role),
	}

	if err := r.client.RPC(ctx, "set_room_role", args, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// RequestSpeak memanggil fungsi request_speak.
func (r *RoomRepository) RequestSpeak(ctx context.Context, roomID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	if err := r.client.RPC(ctx, "request_speak",
		map[string]any{"p_room": roomID}, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

// SetMuted memanggil fungsi set_room_muted.
func (r *RoomRepository) SetMuted(ctx context.Context, roomID string, muted bool) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}

	args := map[string]any{"p_room": roomID, "p_muted": muted}

	if err := r.client.RPC(ctx, "set_room_muted", args, nil, actor); err != nil {
		return translateRoom(err)
	}
	return nil
}

func translateRoom(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateRoomFull:
		return room.ErrFull
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return room.ErrNotAllowed
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return room.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return room.ErrInvalidInput
	default:
		return err
	}
}
