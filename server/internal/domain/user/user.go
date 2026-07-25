// Package user adalah direktori pengguna dan graf pertemanan.
//
// Dipakai untuk menukar hasil scan QR menjadi profil, memulai percakapan, dan
// mengelola siapa yang diikuti. Sengaja terpisah dari autentikasi: package
// internal/auth mengurus siapa pemanggilnya, package ini mengurus orang lain.
package user

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("user: pengguna tidak ditemukan")
	ErrInvalidInput = errors.New("user: input tidak valid")
	ErrNotAllowed   = errors.New("user: tindakan tidak diizinkan")
	ErrSelfFollow   = errors.New("user: tidak bisa mengikuti diri sendiri")
)

// MaxUsernameLength membatasi panjang username yang diterima.
const MaxUsernameLength = 32

// FollowStatus adalah keadaan hubungan mengikuti.
//
// FollowNone bernilai string kosong dengan sengaja: itu keadaan bawaan, dan
// tidak perlu baris di tabel untuk mewakilinya.
type FollowStatus string

const (
	FollowNone     FollowStatus = ""
	FollowPending  FollowStatus = "pending"
	FollowAccepted FollowStatus = "accepted"
)

// Profile adalah informasi publik seorang pengguna.
//
// Hanya berisi yang aman dilihat siapa saja. Email, nomor telepon, dan tanggal
// lahir sengaja tidak ada di sini — kalau nanti dibutuhkan untuk layar
// pengaturan, buatkan tipe terpisah untuk profil milik sendiri.
type Profile struct {
	ID            string
	Username      string
	DisplayName   string
	AvatarMediaID string
	// CoverMediaID adalah storage_key gambar latar/background profil (seperti
	// AvatarMediaID). Kosong berarti tidak ada — klien pakai gradient bawaan.
	CoverMediaID string

	FollowerCount  int
	FollowingCount int

	// FollowStatus dinilai dari sudut pandang pemanggil, bukan sifat profil
	// itu sendiri. Klien memakainya untuk memutuskan tombolnya "Follow",
	// "Requested", atau "Following".
	FollowStatus FollowStatus
	IsSelf       bool

	FollowedAt time.Time
}

// Visitor adalah satu orang yang mengunjungi profil, plus total pengunjung.
type Visitor struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarKey   string
	VisitedAt   time.Time
	// Total adalah jumlah seluruh pengunjung (sama di setiap baris hasil).
	Total int
}

// Repository adalah port penyimpanan.
type Repository interface {
	FindByUsername(ctx context.Context, username string) (Profile, error)
	SearchUsers(ctx context.Context, query string, limit int) ([]Profile, error)
	Follow(ctx context.Context, targetID string) (FollowStatus, error)
	Unfollow(ctx context.Context, targetID string) error
	ListFollowing(ctx context.Context) ([]Profile, error)
	ListFollowers(ctx context.Context, username string) ([]Profile, error)
	ListFollowRequests(ctx context.Context) ([]Profile, error)
	DecideFollowRequest(ctx context.Context, followerID string, approve bool) error

	// RecordVisit mencatat bahwa pemanggil membuka profil profileID.
	RecordVisit(ctx context.Context, profileID string) error
	// ListVisitors mengembalikan pengunjung terbaru profil pemanggil.
	ListVisitors(ctx context.Context, limit int) ([]Visitor, error)
}

// Service memuat alur bisnis direktori pengguna.
type Service struct {
	repo Repository
}

// NewService merangkai service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// FindByUsername mencari profil berdasarkan username.
//
// Username disimpan sebagai citext di database, jadi pencarian tidak
// membedakan huruf besar-kecil — hasil scan QR tidak perlu dinormalkan klien.
func (s *Service) FindByUsername(ctx context.Context, username string) (Profile, error) {
	username, err := normalize(username)
	if err != nil {
		return Profile{}, err
	}
	return s.repo.FindByUsername(ctx, username)
}

// Follow mulai mengikuti seseorang berdasarkan username.
//
// Mengembalikan status hasilnya: `accepted` untuk akun publik, `pending` untuk
// akun privat yang masih harus menyetujui. Idempoten — memanggilnya lagi
// mengembalikan status yang sedang berlaku tanpa menggandakan apa pun.
func (s *Service) Follow(ctx context.Context, username string) (Profile, error) {
	profile, err := s.FindByUsername(ctx, username)
	if err != nil {
		return Profile{}, err
	}
	if profile.IsSelf {
		return Profile{}, ErrSelfFollow
	}

	status, err := s.repo.Follow(ctx, profile.ID)
	if err != nil {
		return Profile{}, err
	}

	profile.FollowStatus = status
	if status == FollowAccepted {
		profile.FollowerCount++
	}
	return profile, nil
}

// Unfollow berhenti mengikuti seseorang. Idempoten.
func (s *Service) Unfollow(ctx context.Context, username string) (Profile, error) {
	profile, err := s.FindByUsername(ctx, username)
	if err != nil {
		return Profile{}, err
	}
	if profile.IsSelf {
		return Profile{}, ErrSelfFollow
	}

	if err := s.repo.Unfollow(ctx, profile.ID); err != nil {
		return Profile{}, err
	}

	if profile.FollowStatus == FollowAccepted && profile.FollowerCount > 0 {
		profile.FollowerCount--
	}
	profile.FollowStatus = FollowNone
	return profile, nil
}

// ListFollowing mengembalikan orang-orang yang diikuti pemanggil.
//
// Ini juga alat diagnosis: kalau story seseorang tidak muncul di story row,
// yang pertama diperiksa adalah apakah ia ada di daftar ini dengan status
// `accepted` — sebab list_stories menyaring berdasarkan itu.
func (s *Service) ListFollowing(ctx context.Context) ([]Profile, error) {
	return s.repo.ListFollowing(ctx)
}

// Search menemukan pengguna berdasarkan username atau nama tampilan. Query
// kosong mengembalikan saran (discovery). limit di-clamp di lapisan SQL.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]Profile, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 30
	}
	return s.repo.SearchUsers(ctx, query, limit)
}

// RecordVisit mencatat bahwa pemanggil membuka profil profileID. Best effort:
// kegagalan tidak boleh menggagalkan tampilan profil, jadi pemanggil di handler
// menjalankannya fire-and-forget. Kunjungan ke diri sendiri disaring di database.
func (s *Service) RecordVisit(ctx context.Context, profileID string) error {
	if profileID == "" {
		return ErrInvalidInput
	}
	return s.repo.RecordVisit(ctx, profileID)
}

// Visitors mengembalikan pengunjung terbaru profil pemanggil.
func (s *Service) Visitors(ctx context.Context, limit int) ([]Visitor, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.repo.ListVisitors(ctx, limit)
}

// ListFollowers mengembalikan pengikut sebuah pengguna.
//
// username kosong berarti pengikut pemanggil sendiri. Hanya yang berstatus
// `accepted` — permintaan follow yang masih menunggu bukan pengikut.
func (s *Service) ListFollowers(ctx context.Context, username string) ([]Profile, error) {
	if username != "" {
		if _, err := normalize(username); err != nil {
			return nil, err
		}
	}
	return s.repo.ListFollowers(ctx, username)
}

// FollowRequests mengembalikan permintaan follow yang menunggu keputusan.
//
// Hanya relevan untuk akun privat. Tanpa endpoint ini, status `pending`
// menggantung selamanya dan pemintanya tidak pernah bisa melihat story.
func (s *Service) FollowRequests(ctx context.Context) ([]Profile, error) {
	return s.repo.ListFollowRequests(ctx)
}

// DecideFollowRequest menyetujui atau menolak permintaan follow.
func (s *Service) DecideFollowRequest(ctx context.Context, username string, approve bool) error {
	profile, err := s.FindByUsername(ctx, username)
	if err != nil {
		return err
	}
	return s.repo.DecideFollowRequest(ctx, profile.ID, approve)
}

func normalize(username string) (string, error) {
	username = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(username), "@"))

	if username == "" || len(username) > MaxUsernameLength {
		return "", ErrInvalidInput
	}
	return username, nil
}
