// Package room memuat aturan bisnis voice room.
//
// BATASAN YANG HARUS DIPAHAMI SEBELUM MEMBACA LEBIH JAUH:
//
// Package ini TIDAK mengalirkan suara, dan tidak akan pernah. Audio realtime
// butuh UDP, jitter buffer, echo cancellation, dan koreksi paket hilang —
// tak satu pun bisa dilakukan lewat WebSocket berbasis TCP atau lewat
// PostgreSQL. Mencoba mengalirkan audio lewat backend Go akan menghasilkan
// suara yang terpotong-potong dengan jeda beberapa detik.
//
// Yang dilakukan package ini: menjadi OTORITAS. Ia menentukan siapa boleh
// masuk, siapa boleh bicara, dan menerbitkan token yang membuktikan itu.
// Suaranya sendiri dibawa SFU (LiveKit/mediasoup/Janus) langsung antar-klien.
//
//	klien ──audio (UDP/WebRTC)──► SFU ──audio──► klien lain
//	  │                            ▲
//	  └──token & peran (HTTPS)──► backend ini
//
// Selengkapnya di docs/voice-rooms.md.
package room

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
)

var (
	ErrNotFound     = errors.New("room: room tidak ditemukan")
	ErrInvalidInput = errors.New("room: input tidak valid")
	ErrNotAllowed   = errors.New("room: tidak diizinkan")
	ErrFull         = errors.New("room: room sudah penuh")
	ErrNoSFU        = errors.New("room: media server belum dikonfigurasi")
)

// MaxTitleLength membatasi panjang judul room.
const MaxTitleLength = 100

// Visibility menentukan siapa yang boleh melihat dan masuk.
type Visibility string

const (
	VisibilityPublic     Visibility = "public"
	VisibilityFollowers  Visibility = "followers"
	VisibilityInviteOnly Visibility = "invite_only"
)

// Role menentukan hak bicara di dalam room.
type Role string

const (
	RoleHost      Role = "host"
	RoleModerator Role = "moderator"
	RoleSpeaker   Role = "speaker"
	RoleListener  Role = "listener"
)

// CanPublish menandai peran yang boleh menerbitkan audio.
//
// Dipakai saat menerbitkan token SFU. Inilah yang membuat "raise hand"
// bermakna: pendengar secara teknis tidak bisa bersuara, bukan sekadar
// tombolnya disembunyikan di UI.
func (r Role) CanPublish() bool {
	return r == RoleHost || r == RoleModerator || r == RoleSpeaker
}

// Room adalah satu sesi voice room.
type Room struct {
	ID           string
	HostID       string
	HostUsername string
	HostName     string
	HostAvatarID string

	Title      string
	Topic      string
	Visibility Visibility

	ParticipantCount int
	SpeakerCount     int
	MaxParticipants  int

	SFURoomID string
	StartedAt time.Time
}

// Participant adalah satu orang di dalam room.
type Participant struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarID    string
	Role        Role
	IsMuted     bool
	JoinedAt    time.Time
}

// Join adalah hasil bergabung: peran, dan bekal untuk menyambung ke SFU.
type Join struct {
	RoomID    string
	Role      Role
	SFURoomID string

	// Token dan URL inilah yang sebenarnya dipakai klien untuk terhubung ke
	// media server dan mulai mendengar suara.
	SFUToken string
	SFUURL   string

	CanPublish bool
}

// Repository adalah port penyimpanan.
type Repository interface {
	Create(ctx context.Context, r Room) error
	List(ctx context.Context) ([]Room, error)
	Join(ctx context.Context, roomID string) (Role, string, error)
	Leave(ctx context.Context, roomID string) error
	ListParticipants(ctx context.Context, roomID string) ([]Participant, error)
	SetRole(ctx context.Context, roomID, targetID string, role Role) error
	RequestSpeak(ctx context.Context, roomID string) error
	SetMuted(ctx context.Context, roomID string, muted bool) error
}

// TokenIssuer menerbitkan kredensial masuk ke media server.
//
// Interface, bukan tipe konkret, supaya SFU-nya bisa ditukar tanpa menyentuh
// domain — LiveKit hari ini, mediasoup besok, tanpa perubahan di sini.
type TokenIssuer interface {
	Issue(roomID, userID, identity string, canPublish bool) (token, url string, err error)
	Configured() bool
}

// Service memuat alur bisnis voice room.
type Service struct {
	repo   Repository
	issuer TokenIssuer
}

// NewService merangkai service.
func NewService(repo Repository, issuer TokenIssuer) *Service {
	return &Service{repo: repo, issuer: issuer}
}

// SFUReady menandai apakah media server sudah dikonfigurasi.
//
// Dipakai handler untuk memberi tahu klien lebih awal: room tetap bisa dibuat
// dan didaftar tanpa SFU, tetapi tidak akan ada suara sama sekali.
func (s *Service) SFUReady() bool { return s.issuer.Configured() }

// CreateInput adalah permintaan membuat room.
type CreateInput struct {
	HostID     string
	Title      string
	Topic      string
	Visibility Visibility
}

// Create membuka room baru dengan pemanggil sebagai host.
func (s *Service) Create(ctx context.Context, in CreateInput) (Room, error) {
	in.Title = strings.TrimSpace(in.Title)

	switch {
	case in.HostID == "" || in.Title == "":
		return Room{}, ErrInvalidInput
	case utf8.RuneCountInString(in.Title) > MaxTitleLength:
		return Room{}, ErrInvalidInput
	}

	switch in.Visibility {
	case VisibilityPublic, VisibilityFollowers, VisibilityInviteOnly:
	case "":
		in.Visibility = VisibilityPublic
	default:
		return Room{}, ErrInvalidInput
	}

	roomID := id.New()

	r := Room{
		ID:         roomID,
		HostID:     in.HostID,
		Title:      in.Title,
		Topic:      strings.TrimSpace(in.Topic),
		Visibility: in.Visibility,

		// Id room di SFU sengaja dibuat sama dengan id room di sini. Dengan
		// begitu tidak ada tabel pemetaan yang harus dijaga tetap sinkron,
		// dan menelusuri masalah antara dua sistem jadi jauh lebih mudah.
		SFURoomID: roomID,
		StartedAt: time.Now().UTC(),
	}

	if err := s.repo.Create(ctx, r); err != nil {
		return Room{}, err
	}
	return r, nil
}

// List mengembalikan room yang sedang berlangsung dan boleh dilihat pemanggil.
func (s *Service) List(ctx context.Context) ([]Room, error) {
	return s.repo.List(ctx)
}

// Join memasukkan pemanggil ke room dan menerbitkan token SFU.
//
// Token inilah yang membuat suara benar-benar terdengar: tanpa itu klien tidak
// punya izin menyambung ke media server, dan yang tersisa hanya daftar nama
// tanpa audio.
func (s *Service) Join(ctx context.Context, roomID, userID, identity string) (Join, error) {
	if roomID == "" || userID == "" {
		return Join{}, ErrInvalidInput
	}

	role, sfuRoomID, err := s.repo.Join(ctx, roomID)
	if err != nil {
		return Join{}, err
	}

	if sfuRoomID == "" {
		sfuRoomID = roomID
	}

	result := Join{
		RoomID:     roomID,
		Role:       role,
		SFURoomID:  sfuRoomID,
		CanPublish: role.CanPublish(),
	}

	// Tanpa SFU terkonfigurasi, keanggotaan tetap tercatat tetapi token kosong.
	// Klien harus memperlakukan token kosong sebagai "belum ada suara", bukan
	// mencoba menyambung dengan string kosong.
	if !s.issuer.Configured() {
		return result, nil
	}

	token, url, err := s.issuer.Issue(sfuRoomID, userID, identity, result.CanPublish)
	if err != nil {
		return Join{}, err
	}

	result.SFUToken = token
	result.SFUURL = url
	return result, nil
}

// Leave mengeluarkan pemanggil. Kalau ia host, room ikut berakhir.
func (s *Service) Leave(ctx context.Context, roomID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}
	return s.repo.Leave(ctx, roomID)
}

// Participants mengembalikan daftar peserta aktif, host lebih dulu.
func (s *Service) Participants(ctx context.Context, roomID string) ([]Participant, error) {
	if roomID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListParticipants(ctx, roomID)
}

// SetRole mengubah peran peserta. Hanya host dan moderator yang boleh.
//
// Perubahan peran berarti token SFU lama sudah tidak sesuai — klien yang
// dipromosikan harus meminta token baru lewat Join agar bisa menerbitkan audio.
func (s *Service) SetRole(ctx context.Context, roomID, targetID string, role Role) error {
	if roomID == "" || targetID == "" {
		return ErrInvalidInput
	}

	switch role {
	case RoleModerator, RoleSpeaker, RoleListener:
	default:
		return ErrInvalidInput
	}

	return s.repo.SetRole(ctx, roomID, targetID, role)
}

// RequestSpeak mengangkat tangan.
func (s *Service) RequestSpeak(ctx context.Context, roomID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}
	return s.repo.RequestSpeak(ctx, roomID)
}

// SetMuted mengubah status bisu diri sendiri.
func (s *Service) SetMuted(ctx context.Context, roomID string, muted bool) error {
	if roomID == "" {
		return ErrInvalidInput
	}
	return s.repo.SetMuted(ctx, roomID, muted)
}
