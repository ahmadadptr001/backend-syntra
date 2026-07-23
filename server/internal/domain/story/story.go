// Package story memuat aturan bisnis story (status 24 jam).
//
// Story adalah bagian layar utama aplikasi, bukan fitur tambahan: deretan
// avatar di bawah header, dengan ring bersegmen sejumlah story yang dimiliki
// orang tersebut. Bentuk data di package ini mengikuti kebutuhan itu — daftar
// yang sudah dikelompokkan per penulis, bukan daftar story mentah.
package story

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
)

var (
	ErrNotFound     = errors.New("story: tidak ditemukan")
	ErrInvalidInput = errors.New("story: input tidak valid")
	ErrMediaNotOwn  = errors.New("story: media bukan milik pengguna ini")
	ErrNotOwner     = errors.New("story: story bukan milik pengguna ini")
)

// Lifetime adalah umur sebuah story.
const Lifetime = 24 * time.Hour

const (
	defaultViewerPage = 50
	maxViewerPage     = 200
)

// Visibility menentukan siapa yang boleh melihat.
type Visibility string

const (
	VisibilityPublic       Visibility = "public"
	VisibilityFollowers    Visibility = "followers"
	VisibilityCloseFriends Visibility = "close_friends"
)

// Story adalah satu unggahan.
type Story struct {
	ID string

	AuthorID       string
	AuthorUsername string
	AuthorName     string
	AuthorAvatarID string

	MediaID    string
	MediaKind  string // image | video
	StorageKey string
	DurationMs int

	Visibility Visibility

	CreatedAt time.Time
	ExpiresAt time.Time

	// Viewed dinilai dari sudut pandang pemanggil, bukan sifat story itu
	// sendiri. Klien memakainya untuk menentukan ring berwarna atau abu.
	Viewed bool
}

// Group adalah seluruh story aktif milik satu orang, urut dari yang terlama.
//
// Inilah bentuk yang langsung dipakai story row: satu Group = satu avatar,
// len(Stories) = jumlah segmen ring, AllViewed = ring abu atau berwarna.
type Group struct {
	AuthorID      string
	Username      string
	DisplayName   string
	AvatarMediaID string
	Stories       []Story
	AllViewed     bool
	LatestStoryAt time.Time
	IsCurrentUser bool
	UnviewedCount int
}

// Mine adalah story milik sendiri, termasuk yang sudah kedaluwarsa.
//
// Dipisahkan dari Story karena sudut pandangnya berbeda: di sini yang menarik
// adalah berapa orang menontonnya dan apakah masih tayang — bukan apakah
// pemanggil sudah menontonnya.
type Mine struct {
	ID         string
	MediaID    string
	MediaKind  string
	StorageKey string
	DurationMs int
	ViewCount  int
	CreatedAt  time.Time
	ExpiresAt  time.Time
	IsExpired  bool
}

// Viewer adalah satu orang yang menonton story.
type Viewer struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarKey   string
	ViewedAt    time.Time
}

// ViewerCursor menandai posisi terakhir saat memuat halaman berikutnya.
//
// Terdiri dari waktu DAN id: dua orang bisa menonton pada milidetik yang sama,
// dan cursor berbasis waktu saja akan melewatkan salah satunya.
type ViewerCursor struct {
	ViewedAt time.Time
	UserID   string
}

// IsZero menandai permintaan halaman pertama.
func (c ViewerCursor) IsZero() bool { return c.ViewedAt.IsZero() }

// Repository adalah port penyimpanan.
type Repository interface {
	Create(ctx context.Context, s Story) error
	ListActive(ctx context.Context, userID string) ([]Story, error)
	ListMine(ctx context.Context, userID string, includeExpired bool) ([]Mine, error)
	ListViewers(ctx context.Context, storyID, userID string, before ViewerCursor, limit int) ([]Viewer, error)
	MarkViewed(ctx context.Context, storyID, userID string) error
	Delete(ctx context.Context, storyID, userID string) error
}

// Service memuat alur bisnis story.
type Service struct {
	repo Repository
}

// NewService merangkai service dengan port yang dibutuhkannya.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create menyimpan story baru dari media yang sudah diunggah.
//
// Media harus sudah terdaftar lebih dulu lewat alur unggah — story hanya
// menunjuk ke media, tidak pernah membawa byte-nya.
func (s *Service) Create(ctx context.Context, userID, mediaID string, visibility Visibility) (Story, error) {
	if userID == "" || mediaID == "" {
		return Story{}, ErrInvalidInput
	}

	switch visibility {
	case VisibilityPublic, VisibilityFollowers, VisibilityCloseFriends:
	case "":
		visibility = VisibilityFollowers
	default:
		return Story{}, ErrInvalidInput
	}

	now := time.Now().UTC()
	st := Story{
		ID:         id.New(),
		AuthorID:   userID,
		MediaID:    mediaID,
		Visibility: visibility,
		CreatedAt:  now,
		ExpiresAt:  now.Add(Lifetime),
	}

	if err := s.repo.Create(ctx, st); err != nil {
		return Story{}, err
	}

	return st, nil
}

// ListGrouped mengembalikan story aktif yang boleh dilihat pengguna,
// dikelompokkan per penulis.
//
// Pengelompokan dilakukan di sini, bukan di database, karena SQL tidak punya
// bentuk hasil bersarang — dan mengirim daftar rata lalu mengelompokkannya di
// klien berarti setiap platform klien harus menulis logika yang sama.
func (s *Service) ListGrouped(ctx context.Context, userID string) ([]Group, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}

	stories, err := s.repo.ListActive(ctx, userID)
	if err != nil {
		return nil, err
	}

	groups := make([]Group, 0, 8)
	index := make(map[string]int, 8)

	for _, st := range stories {
		pos, seen := index[st.AuthorID]
		if !seen {
			groups = append(groups, Group{
				AuthorID:      st.AuthorID,
				Username:      st.AuthorUsername,
				DisplayName:   firstNonEmpty(st.AuthorName, st.AuthorUsername),
				AvatarMediaID: st.AuthorAvatarID,
				IsCurrentUser: st.AuthorID == userID,
				AllViewed:     true,
			})
			pos = len(groups) - 1
			index[st.AuthorID] = pos
		}

		g := &groups[pos]
		g.Stories = append(g.Stories, st)

		if !st.Viewed {
			g.AllViewed = false
			g.UnviewedCount++
		}
		if st.CreatedAt.After(g.LatestStoryAt) {
			g.LatestStoryAt = st.CreatedAt
		}
	}

	return groups, nil
}

// MarkViewed mencatat bahwa pengguna sudah menonton sebuah story.
func (s *Service) MarkViewed(ctx context.Context, storyID, userID string) error {
	if storyID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.MarkViewed(ctx, storyID, userID)
}

// ListMine mengembalikan story milik pemanggil sendiri.
//
// includeExpired dipakai layar arsip; untuk story row cukup yang masih tayang.
func (s *Service) ListMine(ctx context.Context, userID string, includeExpired bool) ([]Mine, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListMine(ctx, userID, includeExpired)
}

// Viewers mengembalikan daftar penonton sebuah story, terbaru dulu.
//
// Hanya pemilik story yang boleh melihatnya. Membukanya ke penonton lain
// berarti memberi tahu siapa saja yang menyimak seseorang — informasi yang
// tidak pernah mereka setujui untuk dibagikan.
func (s *Service) Viewers(ctx context.Context, storyID, userID string, before ViewerCursor, limit int) ([]Viewer, error) {
	if storyID == "" || userID == "" {
		return nil, ErrInvalidInput
	}

	switch {
	case limit <= 0:
		limit = defaultViewerPage
	case limit > maxViewerPage:
		limit = maxViewerPage
	}

	return s.repo.ListViewers(ctx, storyID, userID, before, limit)
}

// Delete menghapus story milik pemanggil.
//
// Soft delete: barisnya tetap ada dengan deleted_at terisi, dan medianya tidak
// ikut dibuang. Permintaan moderasi bisa datang setelah story hilang dari
// layar, dan satu media boleh dipakai di tempat lain — menghapus byte-nya
// seketika akan merusak tautan yang masih dipakai.
func (s *Service) Delete(ctx context.Context, storyID, userID string) error {
	if storyID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.Delete(ctx, storyID, userID)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
