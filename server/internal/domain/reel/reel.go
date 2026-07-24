// Package reel memuat aturan bisnis reels / shorts (video pendek vertikal).
//
// Seperti story, package ini tidak pernah menyentuh byte video — reel hanya
// menunjuk ke media yang sudah diunggah. Yang dijaga di sini adalah bentuk
// data yang langsung dipakai layar Shorts (feed vertikal) dan aturan siapa
// boleh melihat/berinteraksi dengan apa.
package reel

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

var (
	ErrNotFound     = errors.New("reel: tidak ditemukan")
	ErrInvalidInput = errors.New("reel: input tidak valid")
	ErrNotAllowed   = errors.New("reel: tidak diizinkan")
)

const (
	MaxCaption     = 2200
	MaxCommentBody = 1000

	defaultFeedPage    = 20
	maxFeedPage        = 50
	defaultCommentPage = 30
	maxCommentPage     = 100
)

// Visibility menentukan siapa yang boleh melihat reel.
type Visibility string

const (
	VisibilityPublic    Visibility = "public"
	VisibilityFollowers Visibility = "followers"
	VisibilityPrivate   Visibility = "private"
)

// Valid memeriksa visibilitas yang dikenal.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPublic, VisibilityFollowers, VisibilityPrivate:
		return true
	default:
		return false
	}
}

// Reel adalah satu unggahan video pendek beserta status interaksi pemanggil.
type Reel struct {
	ID string

	AuthorID       string
	AuthorUsername string
	AuthorName     string
	AuthorAvatarID string

	MediaID    string
	MediaKind  string // video | image
	StorageKey string
	DurationMs int

	Caption         string
	Visibility      Visibility
	CommentsEnabled bool

	LikeCount    int
	CommentCount int
	ViewCount    int
	ShareCount   int

	// Dari sudut pandang pemanggil, bukan sifat reel itu sendiri.
	Liked       bool
	Saved       bool
	IsFollowing bool

	PublishedAt time.Time
}

// Comment adalah satu komentar pada reel.
type Comment struct {
	ID              string
	ReelID          string
	AuthorID        string
	AuthorUsername  string
	AuthorName      string
	AuthorAvatarID  string
	ParentCommentID string
	Body            string
	LikeCount       int
	CreatedAt       time.Time
}

// Cursor gabungan waktu + id untuk paginasi yang stabil. Dua reel bisa terbit
// pada milidetik yang sama; cursor berbasis waktu saja akan melewatkan salah
// satunya.
type Cursor struct {
	At time.Time
	ID string
}

// IsZero menandai permintaan halaman pertama.
func (c Cursor) IsZero() bool { return c.At.IsZero() }

// CreateInput adalah masukan pembuatan reel.
type CreateInput struct {
	UserID          string
	MediaID         string
	Caption         string
	Visibility      Visibility
	CommentsEnabled bool
}

// Repository adalah port penyimpanan.
type Repository interface {
	Create(ctx context.Context, r Reel) error
	Feed(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error)
	Get(ctx context.Context, reelID, userID string) (Reel, error)
	Mine(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error)
	ListByUser(ctx context.Context, username, userID string, before Cursor, limit int) ([]Reel, error)
	ListSaved(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error)
	Delete(ctx context.Context, reelID, userID string) error

	Like(ctx context.Context, reelID, userID string) error
	Unlike(ctx context.Context, reelID, userID string) error
	Save(ctx context.Context, reelID, userID string) error
	Unsave(ctx context.Context, reelID, userID string) error
	RecordView(ctx context.Context, reelID, userID string) error

	AddComment(ctx context.Context, c Comment) error
	ListComments(ctx context.Context, reelID, userID string, before Cursor, limit int) ([]Comment, error)
	DeleteComment(ctx context.Context, commentID, userID string) error
}

// Service memuat alur bisnis reel.
// Publisher adalah port siaran realtime. Domain hanya menyatakan "kabarkan
// kejadian ini ke topik itu"; transportnya (WebSocket) urusan lapisan luar.
type Publisher interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// Event yang disiarkan ke feed global reels:all.
const (
	EventReelNew     = "reel.new"
	EventReelDeleted = "reel.deleted"
)

type Service struct {
	repo Repository
	pub  Publisher
}

// NewService merangkai service.
func NewService(repo Repository, pub Publisher) *Service { return &Service{repo: repo, pub: pub} }

// Create menyimpan reel baru dari media yang sudah diunggah & dikonfirmasi.
//
// Validasi kepemilikan media, jenis (video/gambar), dan status "ready"
// dilakukan di lapisan SQL — di sinilah satu-satunya tempat kebenaran itu ada.
func (s *Service) Create(ctx context.Context, in CreateInput) (Reel, error) {
	if in.UserID == "" || in.MediaID == "" {
		return Reel{}, ErrInvalidInput
	}
	if len(in.Caption) > MaxCaption {
		return Reel{}, ErrInvalidInput
	}
	switch in.Visibility {
	case VisibilityPublic, VisibilityFollowers, VisibilityPrivate:
	case "":
		in.Visibility = VisibilityPublic
	default:
		return Reel{}, ErrInvalidInput
	}

	r := Reel{
		ID:              id.New(),
		AuthorID:        in.UserID,
		MediaID:         in.MediaID,
		Caption:         in.Caption,
		Visibility:      in.Visibility,
		CommentsEnabled: in.CommentsEnabled,
		PublishedAt:     time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, r); err != nil {
		return Reel{}, err
	}

	// Umumkan ke feed global HANYA reel publik: reel followers/private tak boleh
	// bocor ke feed umum. App yang membuka tab Shorts memakainya sebagai pemicu
	// menyisipkan reel baru di puncak feed.
	if r.Visibility == VisibilityPublic && s.pub != nil {
		_ = s.pub.Publish(ctx, topic.ReelsFeed(), EventReelNew, map[string]any{
			"reel_id":   r.ID,
			"author_id": r.AuthorID,
		})
	}
	return r, nil
}

// Feed mengembalikan reel yang boleh dilihat pemanggil, terbaru dulu.
func (s *Service) Feed(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.Feed(ctx, userID, before, clampFeed(limit))
}

// Get mengembalikan satu reel bila pemanggil boleh melihatnya.
func (s *Service) Get(ctx context.Context, reelID, userID string) (Reel, error) {
	if reelID == "" || userID == "" {
		return Reel{}, ErrInvalidInput
	}
	return s.repo.Get(ctx, reelID, userID)
}

// ListMine mengembalikan reel milik pemanggil sendiri.
func (s *Service) ListMine(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.Mine(ctx, userID, before, clampFeed(limit))
}

// ListByUser mengembalikan reel milik seorang pengguna (grid profil).
func (s *Service) ListByUser(ctx context.Context, username, userID string, before Cursor, limit int) ([]Reel, error) {
	if username == "" || userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListByUser(ctx, username, userID, before, clampFeed(limit))
}

// ListSaved mengembalikan reel yang disimpan pemanggil.
func (s *Service) ListSaved(ctx context.Context, userID string, before Cursor, limit int) ([]Reel, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListSaved(ctx, userID, before, clampFeed(limit))
}

// Delete menghapus reel milik pemanggil (soft delete).
func (s *Service) Delete(ctx context.Context, reelID, userID string) error {
	if reelID == "" || userID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.Delete(ctx, reelID, userID); err != nil {
		return err
	}

	// Siarkan penghapusan ke feed global supaya reel hilang dari layar orang lain
	// tanpa refresh. Kalau reel-nya tak publik, event ini menunjuk id yang tak
	// ada di feed mereka — no-op yang tidak berbahaya.
	if s.pub != nil {
		_ = s.pub.Publish(ctx, topic.ReelsFeed(), EventReelDeleted, map[string]any{
			"reel_id": reelID,
		})
	}
	return nil
}

// Like menyukai reel (idempoten).
func (s *Service) Like(ctx context.Context, reelID, userID string) error {
	return s.mustIDs(reelID, userID, func() error { return s.repo.Like(ctx, reelID, userID) })
}

// Unlike membatalkan suka.
func (s *Service) Unlike(ctx context.Context, reelID, userID string) error {
	return s.mustIDs(reelID, userID, func() error { return s.repo.Unlike(ctx, reelID, userID) })
}

// Save menyimpan reel ke bookmark.
func (s *Service) Save(ctx context.Context, reelID, userID string) error {
	return s.mustIDs(reelID, userID, func() error { return s.repo.Save(ctx, reelID, userID) })
}

// Unsave menghapus dari bookmark.
func (s *Service) Unsave(ctx context.Context, reelID, userID string) error {
	return s.mustIDs(reelID, userID, func() error { return s.repo.Unsave(ctx, reelID, userID) })
}

// RecordView mencatat tayangan (di-dedup di database — satu penonton sekali).
func (s *Service) RecordView(ctx context.Context, reelID, userID string) error {
	return s.mustIDs(reelID, userID, func() error { return s.repo.RecordView(ctx, reelID, userID) })
}

// AddComment menambah komentar pada reel.
func (s *Service) AddComment(ctx context.Context, reelID, userID, body, parentID string) (Comment, error) {
	if reelID == "" || userID == "" {
		return Comment{}, ErrInvalidInput
	}
	if l := len(body); l == 0 || l > MaxCommentBody {
		return Comment{}, ErrInvalidInput
	}
	c := Comment{
		ID:              id.New(),
		ReelID:          reelID,
		AuthorID:        userID,
		ParentCommentID: parentID,
		Body:            body,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.AddComment(ctx, c); err != nil {
		return Comment{}, err
	}
	return c, nil
}

// ListComments mengembalikan komentar sebuah reel, terbaru dulu.
func (s *Service) ListComments(ctx context.Context, reelID, userID string, before Cursor, limit int) ([]Comment, error) {
	if reelID == "" || userID == "" {
		return nil, ErrInvalidInput
	}
	switch {
	case limit <= 0:
		limit = defaultCommentPage
	case limit > maxCommentPage:
		limit = maxCommentPage
	}
	return s.repo.ListComments(ctx, reelID, userID, before, limit)
}

// DeleteComment menghapus komentar (penulis komentar atau pemilik reel).
func (s *Service) DeleteComment(ctx context.Context, commentID, userID string) error {
	if commentID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.DeleteComment(ctx, commentID, userID)
}

func (s *Service) mustIDs(reelID, userID string, fn func() error) error {
	if reelID == "" || userID == "" {
		return ErrInvalidInput
	}
	return fn()
}

func clampFeed(limit int) int {
	switch {
	case limit <= 0:
		return defaultFeedPage
	case limit > maxFeedPage:
		return maxFeedPage
	default:
		return limit
	}
}
