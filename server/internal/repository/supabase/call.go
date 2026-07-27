package supabase

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/call"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// CallRepository memenuhi kontrak call.Repository.
type CallRepository struct {
	client *sb.Client
}

// NewCallRepository membuat repository panggilan.
func NewCallRepository(client *sb.Client) *CallRepository {
	return &CallRepository{client: client}
}

var _ call.Repository = (*CallRepository)(nil)

type startCallRow struct {
	CallID    string `json:"call_id"`
	SFURoomID string `json:"sfu_room_id"`
	IsNew     bool   `json:"is_new"`
}

// Start memanggil fungsi start_call.
func (r *CallRepository) Start(ctx context.Context, callID, conversationID, kind, sfuRoom string) (string, string, bool, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return "", "", false, err
	}
	args := map[string]any{
		"p_id":           callID,
		"p_conversation": conversationID,
		"p_kind":         kind,
		"p_sfu_room":     sfuRoom,
	}
	var rows []startCallRow
	if err := r.client.RPC(ctx, "start_call", args, &rows, actor); err != nil {
		return "", "", false, translateCall(err)
	}
	if len(rows) == 0 {
		return "", "", false, call.ErrNotFound
	}
	return rows[0].CallID, rows[0].SFURoomID, rows[0].IsNew, nil
}

// ExpireStale mengakhiri SEMUA panggilan yang belum berakhir di sebuah
// percakapan, langsung lewat PostgREST (service role). Dipakai TEPAT SEBELUM
// membuat panggilan baru di Start.
//
// Tanpa ambang waktu, dan itu memang benar untuk panggilan 1:1: memulai panggilan
// baru secara definisi membatalkan yang lama. Klien pun mencegah memulai panggilan
// saat sedang menelepon (guard isBusy), jadi kalau permintaan Start sampai ke sini
// sementara masih ada baris 'ringing'/'ongoing', baris itu PASTI zombie (aplikasi
// ter-kill, koneksi putus, dsb). Membiarkannya membuat start_call bergabung diam-
// diam ke zombie (is_new=false) sehingga call.incoming tak disiarkan dan lawan tak
// pernah berdering. Best-effort: kegagalan tak menggagalkan panggilan.
func (r *CallRepository) ExpireStale(ctx context.Context, conversationID string) error {
	now := time.Now().UTC()
	body := map[string]any{"status": "ended", "ended_at": now.Format(time.RFC3339)}

	q := url.Values{}
	q.Set("conversation_id", "eq."+conversationID)
	q.Set("status", "in.(ringing,ongoing)")
	q.Set("ended_at", "is.null")

	if err := r.client.Update(ctx, "calls", body, nil, sb.WithServiceRole(), sb.WithQuery(q)); err != nil {
		return translateCall(err)
	}
	return nil
}

// Invite memanggil invite_to_call.
func (r *CallRepository) Invite(ctx context.Context, callID, targetID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	args := map[string]any{"p_call": callID, "p_target": targetID}
	if err := r.client.RPC(ctx, "invite_to_call", args, nil, actor); err != nil {
		return translateCall(err)
	}
	return nil
}

type callParticipantRow struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Joined      bool   `json:"joined"`
}

// Participants memanggil list_call_participants.
func (r *CallRepository) Participants(ctx context.Context, callID string) ([]call.Participant, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}
	var rows []callParticipantRow
	if err := r.client.RPC(ctx, "list_call_participants",
		map[string]any{"p_call": callID}, &rows, actor); err != nil {
		return nil, translateCall(err)
	}
	out := make([]call.Participant, 0, len(rows))
	for _, row := range rows {
		out = append(out, call.Participant{
			UserID: row.UserID, Username: row.Username,
			DisplayName: row.DisplayName, Joined: row.Joined,
		})
	}
	return out, nil
}

// Answer memanggil fungsi answer_call.
func (r *CallRepository) Answer(ctx context.Context, callID string) (string, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return "", err
	}
	var sfu string
	if err := r.client.RPC(ctx, "answer_call", map[string]any{"p_call": callID}, &sfu, actor); err != nil {
		return "", translateCall(err)
	}
	return sfu, nil
}

// Decline memanggil fungsi decline_call.
func (r *CallRepository) Decline(ctx context.Context, callID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "decline_call", map[string]any{"p_call": callID}, nil, actor); err != nil {
		return translateCall(err)
	}
	return nil
}

// Leave memanggil fungsi leave_call.
func (r *CallRepository) Leave(ctx context.Context, callID string) error {
	actor, err := callerOption(ctx)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "leave_call", map[string]any{"p_call": callID}, nil, actor); err != nil {
		return translateCall(err)
	}
	return nil
}

type activeCallRow struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	InitiatorID string    `json:"initiator_id"`
	SFURoomID   *string   `json:"sfu_room_id"`
	StartedAt   time.Time `json:"started_at"`
}

// GetActive memanggil fungsi get_active_call. Mengembalikan nil kalau tidak ada.
func (r *CallRepository) GetActive(ctx context.Context, conversationID string) (*call.Active, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}
	var rows []activeCallRow
	if err := r.client.RPC(ctx, "get_active_call",
		map[string]any{"p_conversation": conversationID}, &rows, actor); err != nil {
		return nil, translateCall(err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row := rows[0]
	return &call.Active{
		ID: row.ID, Kind: call.Kind(row.Kind), Status: row.Status,
		InitiatorID: row.InitiatorID, SFURoomID: deref(row.SFURoomID), StartedAt: row.StartedAt,
	}, nil
}

type sfuResultRow struct {
	CallID         string `json:"out_call_id"`
	ConversationID string `json:"out_conversation_id"`
	Ended          bool   `json:"out_ended"`
}

// SFUParticipantLeft memanggil fungsi sfu_participant_left.
//
// Memakai kunci service: webhook LiveKit tidak membawa JWT pengguna, jadi tidak
// ada identitas yang bisa dipinjam. Fungsi SQL-nya hanya diberi izin ke
// service_role, dan pemanggilnya sudah di belakang verifikasi tanda tangan.
func (r *CallRepository) SFUParticipantLeft(ctx context.Context, sfuRoom, identity string) (*call.SFUResult, error) {
	return r.sfuClose(ctx, "sfu_participant_left", map[string]any{
		"p_sfu_room": sfuRoom,
		"p_identity": identity,
	})
}

// SFURoomFinished memanggil fungsi sfu_room_finished.
func (r *CallRepository) SFURoomFinished(ctx context.Context, sfuRoom string) (*call.SFUResult, error) {
	return r.sfuClose(ctx, "sfu_room_finished", map[string]any{
		"p_sfu_room": sfuRoom,
	})
}

func (r *CallRepository) sfuClose(ctx context.Context, fn string, args map[string]any) (*call.SFUResult, error) {
	var rows []sfuResultRow
	if err := r.client.RPC(ctx, fn, args, &rows, sb.WithServiceRole()); err != nil {
		return nil, translateCall(err)
	}
	if len(rows) == 0 {
		return nil, nil // tidak ada panggilan aktif untuk room ini
	}
	row := rows[0]
	return &call.SFUResult{
		CallID: row.CallID, ConversationID: row.ConversationID, Ended: row.Ended,
	}, nil
}

func translateCall(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return call.ErrNotAllowed
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return call.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return call.ErrInvalidInput
	default:
		return err
	}
}
