// Package notification mengurus pemberitahuan dalam aplikasi.
//
// Tabel notifications sudah ada sejak migrasi pertama, dan konstanta protokol
// `notification.new` pun sudah terdefinisi — tetapi tidak ada satu pun yang
// menyiarkan maupun membacanya. Package ini yang menghubungkan keduanya.
//
// Notifikasi disimpan DAN disiarkan. Disimpan supaya tetap ada saat aplikasi
// dibuka lagi; disiarkan supaya badge bergerak seketika tanpa polling.
package notification

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

var (
	ErrInvalidInput = errors.New("notification: input tidak valid")
	ErrNotFound     = errors.New("notification: tidak ditemukan")
)

const (
	defaultPageSize = 30
	maxPageSize     = 100
)

// Type mengikuti enum NOTIFICATIONS.type di docs/erd.md.
type Type string

const (
	TypeFollow     Type = "follow"
	TypeLike       Type = "like"
	TypeComment    Type = "comment"
	TypeMention    Type = "mention"
	TypeStoryReply Type = "story_reply"
	TypeRoomLive   Type = "room_live"
	TypeSystem     Type = "system"
)

// Valid memeriksa apakah jenis notifikasi dikenal.
func (t Type) Valid() bool {
	switch t {
	case TypeFollow, TypeLike, TypeComment, TypeMention, TypeStoryReply, TypeRoomLive, TypeSystem:
		return true
	default:
		return false
	}
}

// Notification adalah satu pemberitahuan.
type Notification struct {
	ID   string
	Type Type

	ActorID        string
	ActorUsername  string
	ActorName      string
	ActorAvatarKey string

	SubjectType string
	SubjectID   string

	IsRead    bool
	CreatedAt time.Time
}

// Event adalah bentuk payload yang disiarkan ke klien.
type Event struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	ActorID     string    `json:"actor_id,omitempty"`
	SubjectType string    `json:"subject_type,omitempty"`
	SubjectID   string    `json:"subject_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Repository adalah port penyimpanan.
type Repository interface {
	List(ctx context.Context, before string, limit int) ([]Notification, error)
	CountUnread(ctx context.Context) (int, error)
	MarkRead(ctx context.Context, notificationID string) (int, error)
	Create(ctx context.Context, n Notification, recipientID string) (bool, error)
}

// Publisher adalah port siaran realtime.
type Publisher interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// Service memuat alur bisnis notifikasi.
type Service struct {
	repo Repository
	pub  Publisher
}

// NewService merangkai service.
func NewService(repo Repository, pub Publisher) *Service {
	return &Service{repo: repo, pub: pub}
}

// List mengembalikan notifikasi pemanggil, terbaru dulu.
//
// before adalah id notifikasi sebagai cursor — id memakai UUIDv7 yang terurut
// waktu, jadi tidak butuh index tambahan dan tidak melewatkan baris ketika dua
// notifikasi lahir pada milidetik yang sama.
func (s *Service) List(ctx context.Context, before string, limit int) ([]Notification, error) {
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}
	return s.repo.List(ctx, before, limit)
}

// CountUnread mengembalikan jumlah yang belum dibaca, untuk badge.
func (s *Service) CountUnread(ctx context.Context) (int, error) {
	return s.repo.CountUnread(ctx)
}

// MarkRead menandai sudah dibaca. notificationID kosong berarti semuanya.
func (s *Service) MarkRead(ctx context.Context, notificationID string) (int, error) {
	return s.repo.MarkRead(ctx, notificationID)
}

// NotifyInput adalah permintaan membuat notifikasi.
type NotifyInput struct {
	RecipientID string
	Type        Type
	SubjectType string
	SubjectID   string
}

// Notify membuat notifikasi lalu menyiarkannya.
//
// Dilewati diam-diam kalau penerimanya adalah pemanggil sendiri, atau kalau
// salah satu pihak memblokir yang lain — keduanya diputuskan di sisi database
// supaya aturannya tidak terduplikasi.
func (s *Service) Notify(ctx context.Context, in NotifyInput) error {
	if in.RecipientID == "" || !in.Type.Valid() {
		return ErrInvalidInput
	}

	n := Notification{
		ID:          id.New(),
		Type:        in.Type,
		SubjectType: in.SubjectType,
		SubjectID:   in.SubjectID,
		CreatedAt:   time.Now().UTC(),
	}

	created, err := s.repo.Create(ctx, n, in.RecipientID)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}

	// Siaran gagal tidak membatalkan notifikasi yang sudah tersimpan — ia akan
	// terlihat saat daftar dimuat berikutnya.
	_ = s.pub.Publish(ctx, topic.User(in.RecipientID), "notification.new", Event{
		ID:          n.ID,
		Type:        string(n.Type),
		SubjectType: n.SubjectType,
		SubjectID:   n.SubjectID,
		CreatedAt:   n.CreatedAt,
	})

	return nil
}
