package supabase

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/notification"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// NotificationRepository memenuhi kontrak notification.Repository.
type NotificationRepository struct {
	client *sb.Client
}

// NewNotificationRepository membuat repository notifikasi.
func NewNotificationRepository(client *sb.Client) *NotificationRepository {
	return &NotificationRepository{client: client}
}

var _ notification.Repository = (*NotificationRepository)(nil)

type notificationRow struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	ActorID        *string   `json:"actor_id"`
	ActorUsername  string    `json:"actor_username"`
	ActorName      string    `json:"actor_name"`
	ActorAvatarKey string    `json:"actor_avatar_key"`
	SubjectType    string    `json:"subject_type"`
	SubjectID      *string   `json:"subject_id"`
	IsRead         bool      `json:"is_read"`
	CreatedAt      time.Time `json:"created_at"`
}

// List memanggil fungsi list_notifications.
func (r *NotificationRepository) List(ctx context.Context, before string, limit int) ([]notification.Notification, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return nil, err
	}

	args := map[string]any{
		"p_before": nullIfEmpty(before),
		"p_limit":  limit,
	}

	var rows []notificationRow
	if err := r.client.RPC(ctx, "list_notifications", args, &rows, actor); err != nil {
		return nil, translateNotification(err)
	}

	out := make([]notification.Notification, 0, len(rows))
	for _, row := range rows {
		out = append(out, notification.Notification{
			ID:             row.ID,
			Type:           notification.Type(row.Type),
			ActorID:        deref(row.ActorID),
			ActorUsername:  row.ActorUsername,
			ActorName:      row.ActorName,
			ActorAvatarKey: row.ActorAvatarKey,
			SubjectType:    row.SubjectType,
			SubjectID:      deref(row.SubjectID),
			IsRead:         row.IsRead,
			CreatedAt:      row.CreatedAt,
		})
	}
	return out, nil
}

// CountUnread memanggil fungsi count_unread_notifications.
func (r *NotificationRepository) CountUnread(ctx context.Context) (int, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return 0, err
	}

	var n int
	if err := r.client.RPC(ctx, "count_unread_notifications", map[string]any{}, &n, actor); err != nil {
		return 0, translateNotification(err)
	}
	return n, nil
}

// MarkRead memanggil fungsi mark_notifications_read.
func (r *NotificationRepository) MarkRead(ctx context.Context, notificationID string) (int, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return 0, err
	}

	var n int
	if err := r.client.RPC(ctx, "mark_notifications_read",
		map[string]any{"p_notification": nullIfEmpty(notificationID)}, &n, actor); err != nil {
		return 0, translateNotification(err)
	}
	return n, nil
}

// Create memanggil fungsi create_notification.
//
// Mengembalikan false kalau notifikasi sengaja dilewati — penerima adalah
// pemanggil sendiri, atau salah satu pihak memblokir yang lain.
func (r *NotificationRepository) Create(ctx context.Context, n notification.Notification, recipientID string) (bool, error) {
	actor, err := callerOption(ctx)
	if err != nil {
		return false, err
	}

	args := map[string]any{
		"p_id":           n.ID,
		"p_recipient":    recipientID,
		"p_type":         string(n.Type),
		"p_subject_type": n.SubjectType,
		"p_subject":      nullIfEmpty(n.SubjectID),
	}

	var created bool
	if err := r.client.RPC(ctx, "create_notification", args, &created, actor); err != nil {
		return false, translateNotification(err)
	}
	return created, nil
}

func translateNotification(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return notification.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return notification.ErrInvalidInput
	default:
		return err
	}
}
