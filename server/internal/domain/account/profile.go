package account

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// Profil sendiri, blokir, perangkat, dan laporan — semuanya bertumpu pada
// identitas pemanggil, jadi diletakkan di package account bersama alur auth.

var (
	ErrProfileNotFound = errors.New("account: profil tidak ditemukan")
	ErrBadPrivacy      = errors.New("account: nilai privasi tidak valid")
	ErrCannotBlockSelf = errors.New("account: tidak bisa memblokir diri sendiri")
	ErrUsernameTaken   = errors.New("account: username sudah dipakai")
	ErrInvalidUsername = errors.New("account: username tidak valid")
)

const (
	MaxDisplayName = 60
	MaxBio         = 200
)

// MyProfile adalah profil lengkap pemilik akun — termasuk yang tidak boleh
// dilihat orang lain (email, preferensi privasi, tanggal lahir).
type MyProfile struct {
	ID          string
	Username    string
	Email       string
	DisplayName string
	Bio         string
	AvatarKey   string
	CoverKey    string

	FollowerCount  int
	FollowingCount int

	IsPrivate    bool
	DateOfBirth  string
	DMPrivacy    string
	StoryPrivacy string
	Locale       string
}

// BlockedUser adalah satu orang yang diblokir.
type BlockedUser struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarKey   string
}

// UpdateProfileInput memuat perubahan profil. Field nil berarti "biarkan".
type UpdateProfileInput struct {
	DisplayName   *string
	Bio           *string
	AvatarMediaID *string
	CoverMediaID  *string
	Username      *string
	IsPrivate     *bool
	DMPrivacy     *string
	StoryPrivacy  *string
}

// ProfileStore adalah port penyimpanan profil, blokir, perangkat, dan laporan.
type ProfileStore interface {
	GetMyProfile(ctx context.Context) (MyProfile, error)
	UpdateMyProfile(ctx context.Context, in UpdateProfileInput) error

	BlockUser(ctx context.Context, targetID string) error
	UnblockUser(ctx context.Context, targetID string) error
	ListBlocked(ctx context.Context) ([]BlockedUser, error)

	RegisterDevice(ctx context.Context, deviceID, platform, pushToken, appVersion string) error
	RevokeDevice(ctx context.Context, deviceID string) error

	CreateReport(ctx context.Context, targetType, targetID, reason, detail string) error
}

// UserFinder menukar username menjadi id, dipakai blokir yang menerima username.
type UserFinder interface {
	FindID(ctx context.Context, username string) (string, error)
}

// ProfileService memuat alur bisnis profil dan sekitarnya.
type ProfileService struct {
	store  ProfileStore
	finder UserFinder
}

// NewProfileService merangkai service.
func NewProfileService(store ProfileStore, finder UserFinder) *ProfileService {
	return &ProfileService{store: store, finder: finder}
}

// Get mengembalikan profil pemanggil.
func (s *ProfileService) Get(ctx context.Context) (MyProfile, error) {
	return s.store.GetMyProfile(ctx)
}

// Update mengubah profil. Validasi panjang di sini; NULL diteruskan apa adanya
// supaya klien bisa mengirim hanya field yang berubah.
func (s *ProfileService) Update(ctx context.Context, in UpdateProfileInput) error {
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if utf8.RuneCountInString(name) > MaxDisplayName {
			return ErrInvalidInput
		}
		in.DisplayName = &name
	}
	if in.Bio != nil && utf8.RuneCountInString(*in.Bio) > MaxBio {
		return ErrInvalidInput
	}
	if in.DMPrivacy != nil && !validDMPrivacy(*in.DMPrivacy) {
		return ErrBadPrivacy
	}
	if in.StoryPrivacy != nil && !validStoryPrivacy(*in.StoryPrivacy) {
		return ErrBadPrivacy
	}
	if in.Username != nil {
		// Aturan yang SAMA PERSIS dengan pendaftaran (validUsername): kalau
		// lebih ketat, pengguna lama yang username-nya sah saat daftar tapi
		// tak lolos aturan baru akan gagal menyimpan perubahan apa pun begitu
		// layar Edit Profil ikut mengirim username yang tak berubah. Trim saja,
		// tanpa mengecilkan huruf — pendaftaran pun menyimpan apa adanya, dan
		// keunikan lintas-kapital sudah ditegakkan citext.
		uname := strings.TrimSpace(*in.Username)
		if !validUsername(uname) {
			return ErrInvalidUsername
		}
		in.Username = &uname
	}
	return s.store.UpdateMyProfile(ctx, in)
}

// Block memblokir seseorang berdasarkan username.
func (s *ProfileService) Block(ctx context.Context, username string) error {
	targetID, err := s.finder.FindID(ctx, username)
	if err != nil {
		return err
	}
	return s.store.BlockUser(ctx, targetID)
}

// Unblock membatalkan blokir.
func (s *ProfileService) Unblock(ctx context.Context, username string) error {
	targetID, err := s.finder.FindID(ctx, username)
	if err != nil {
		return err
	}
	return s.store.UnblockUser(ctx, targetID)
}

// ListBlocked mengembalikan daftar yang diblokir.
func (s *ProfileService) ListBlocked(ctx context.Context) ([]BlockedUser, error) {
	return s.store.ListBlocked(ctx)
}

// RegisterDevice menyimpan push token perangkat, untuk notifikasi FCM.
func (s *ProfileService) RegisterDevice(ctx context.Context, deviceID, platform, pushToken, appVersion string) error {
	switch platform {
	case "android", "ios", "web":
	default:
		return ErrInvalidInput
	}
	if deviceID == "" || pushToken == "" {
		return ErrInvalidInput
	}
	return s.store.RegisterDevice(ctx, deviceID, platform, pushToken, appVersion)
}

// RevokeDevice mencabut perangkat, misalnya saat logout.
func (s *ProfileService) RevokeDevice(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return ErrInvalidInput
	}
	return s.store.RevokeDevice(ctx, deviceID)
}

// Report melaporkan konten atau pengguna.
func (s *ProfileService) Report(ctx context.Context, targetType, targetID, reason, detail string) error {
	if !validReportTarget(targetType) || !validReportReason(reason) || targetID == "" {
		return ErrInvalidInput
	}
	return s.store.CreateReport(ctx, targetType, targetID, reason, detail)
}

func validDMPrivacy(v string) bool {
	return v == "everyone" || v == "following" || v == "nobody"
}

func validStoryPrivacy(v string) bool {
	return v == "public" || v == "followers" || v == "close_friends"
}

func validReportTarget(v string) bool {
	switch v {
	case "user", "reel", "story", "message", "room", "comment":
		return true
	default:
		return false
	}
}

func validReportReason(v string) bool {
	switch v {
	case "spam", "harassment", "nudity", "violence", "csam", "copyright", "other":
		return true
	default:
		return false
	}
}
