// Package music memuat aturan bisnis katalog musik komunitas: lagu yang diunggah
// pengguna dari perangkatnya lalu terbit publik dan bisa dicari.
//
// Seperti reel dan story, package ini tidak pernah menyentuh byte audio — track
// hanya menunjuk ke media (kind 'audio') yang sudah diunggah & 'ready'. Yang
// dijaga di sini adalah bentuk data yang dipakai tab Musik dan aturan penerbitan.
package music

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

var (
	ErrNotFound     = errors.New("music: tidak ditemukan")
	ErrInvalidInput = errors.New("music: input tidak valid")
	ErrNotAllowed   = errors.New("music: tidak diizinkan")
)

const (
	MaxTitle  = 200
	MaxArtist = 200

	defaultFeedPage = 40
	maxFeedPage     = 100
)

// Track adalah satu lagu komunitas beserta info penulisnya.
type Track struct {
	ID string

	AuthorID       string
	AuthorUsername string
	AuthorName     string

	MediaID    string
	StorageKey string
	// CoverMediaID adalah id media sampul SEBELUM di-resolve (dipakai saat Create).
	CoverMediaID string
	// CoverStorageKey adalah storage_key sampul SETELAH di-resolve (diisi feed/search);
	// kosong bila lagu tak punya sampul.
	CoverStorageKey string

	Title      string
	Artist     string
	DurationMs int

	CreatedAt time.Time
}

// CreateInput adalah masukan penerbitan lagu.
type CreateInput struct {
	UserID       string
	MediaID      string
	CoverMediaID string
	Title        string
	Artist       string
	DurationMs   int
}

// Repository adalah port penyimpanan.
type Repository interface {
	Create(ctx context.Context, t Track) error
	Feed(ctx context.Context, userID string, limit int) ([]Track, error)
	Search(ctx context.Context, userID, query string, limit int) ([]Track, error)
	Delete(ctx context.Context, trackID, userID string) error
	UpdateTitle(ctx context.Context, trackID, userID, title string) error
}

// Publisher adalah port siaran realtime (opsional). Domain hanya menyatakan
// "kabarkan kejadian ini ke topik itu"; transportnya urusan lapisan luar.
type Publisher interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// Event yang disiarkan ke feed musik global.
const EventMusicNew = "music.new"

// Service memuat alur bisnis musik.
type Service struct {
	repo Repository
	pub  Publisher
}

// NewService merangkai service. pub boleh nil (siaran realtime dimatikan).
func NewService(repo Repository, pub Publisher) *Service {
	return &Service{repo: repo, pub: pub}
}

// Create menerbitkan lagu dari audio yang sudah diunggah & dikonfirmasi.
//
// Validasi kepemilikan media, jenis ('audio'), dan status 'ready' dilakukan di
// lapisan SQL — satu-satunya tempat kebenaran itu ada.
func (s *Service) Create(ctx context.Context, in CreateInput) (Track, error) {
	if in.UserID == "" || in.MediaID == "" {
		return Track{}, ErrInvalidInput
	}
	if len(in.Title) > MaxTitle || len(in.Artist) > MaxArtist {
		return Track{}, ErrInvalidInput
	}
	if in.DurationMs < 0 {
		return Track{}, ErrInvalidInput
	}

	t := Track{
		ID:           id.New(),
		AuthorID:     in.UserID,
		MediaID:      in.MediaID,
		CoverMediaID: in.CoverMediaID,
		Title:        in.Title,
		Artist:       in.Artist,
		DurationMs:   in.DurationMs,
		CreatedAt:    time.Now().UTC(),
	}
	// Validitas cover dicek di SQL (cover tak valid diabaikan, bukan menggagalkan
	// penerbitan).
	if err := s.repo.Create(ctx, t); err != nil {
		return Track{}, err
	}

	// Umumkan ke feed musik global supaya rail komunitas bisa hidup tanpa refresh
	// (app boleh belum mendengarkannya — best effort).
	if s.pub != nil {
		_ = s.pub.Publish(ctx, topic.MusicFeed(), EventMusicNew, map[string]any{
			"id":        t.ID,
			"author_id": t.AuthorID,
		})
	}
	return t, nil
}

// Feed mengembalikan katalog publik terbaru.
func (s *Service) Feed(ctx context.Context, userID string, limit int) ([]Track, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.Feed(ctx, userID, clampFeed(limit))
}

// Search mencari lagu publik berdasarkan judul/artis. Query kosong = hasil kosong.
func (s *Service) Search(ctx context.Context, userID, query string, limit int) ([]Track, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.Search(ctx, userID, query, clampFeed(limit))
}

// Delete menghapus lagu milik pemanggil (soft delete).
func (s *Service) Delete(ctx context.Context, trackID, userID string) error {
	if trackID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.Delete(ctx, trackID, userID)
}

// Rename mengubah judul lagu milik pemanggil. Judul dikosongkan/terlalu panjang
// ditolak; kepemilikan dijaga di lapisan SQL.
func (s *Service) Rename(ctx context.Context, trackID, userID, title string) error {
	if trackID == "" || userID == "" {
		return ErrInvalidInput
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > MaxTitle {
		return ErrInvalidInput
	}
	return s.repo.UpdateTitle(ctx, trackID, userID, title)
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
