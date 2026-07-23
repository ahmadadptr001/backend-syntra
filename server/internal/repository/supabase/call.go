package supabase

import (
	"context"
	"errors"
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
