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
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

var (
	ErrNotFound     = errors.New("room: room tidak ditemukan")
	ErrInvalidInput = errors.New("room: input tidak valid")
	ErrNotAllowed   = errors.New("room: tidak diizinkan")
	ErrFull         = errors.New("room: room sudah penuh")
	ErrNoSFU        = errors.New("room: media server belum dikonfigurasi")
)

const (
	// MaxTitleLength membatasi panjang judul room.
	MaxTitleLength = 100

	// defaultMaxParticipants harus sama dengan nilai bawaan kolom
	// rooms.max_participants di migrasi.
	defaultMaxParticipants = 50
)

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

	// AvatarKey adalah storage key, bukan id media. Klien tidak punya cara
	// mengubah id menjadi URL — ia tidak tahu path-nya — sehingga avatar
	// peserta tidak bisa dirender sama sekali kalau hanya id yang dikirim.
	AvatarKey string

	Role          Role
	IsMuted       bool
	HasRaisedHand bool
	JoinedAt      time.Time
}

// SpeakRequest adalah satu permintaan bicara yang menunggu keputusan host.
type SpeakRequest struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarKey   string
	RequestedAt time.Time
}

// JoinStatus membedakan berhasil masuk dari menunggu persetujuan host.
type JoinStatus string

const (
	JoinStatusJoined  JoinStatus = "joined"
	JoinStatusPending JoinStatus = "pending"
)

// Join adalah hasil bergabung: peran, dan bekal untuk menyambung ke SFU.
type Join struct {
	RoomID string

	// Status "pending" berarti permintaan masuk antre menunggu keputusan host.
	// Token SFU sengaja tidak diterbitkan dalam keadaan itu — kalau diterbitkan,
	// penantian di ruang tunggu hanya jadi hiasan yang bisa dilewati siapa pun
	// yang memanggil endpoint langsung.
	Status JoinStatus

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
	Join(ctx context.Context, roomID string) (JoinStatus, Role, string, error)
	End(ctx context.Context, roomID string) error
	CancelSpeakRequest(ctx context.Context, roomID string) error
	ListJoinRequests(ctx context.Context, roomID string) ([]SpeakRequest, error)
	DecideJoinRequest(ctx context.Context, roomID, userID string, approve bool) error

	// Leave mengembalikan true kalau yang keluar adalah host, yang berarti
	// room ikut berakhir dan peserta lain harus diberi tahu.
	Leave(ctx context.Context, roomID string) (endedRoom bool, err error)

	ListParticipants(ctx context.Context, roomID string) ([]Participant, error)
	ListSpeakRequests(ctx context.Context, roomID string) ([]SpeakRequest, error)
	SetRole(ctx context.Context, roomID, targetID string, role Role) error
	RequestSpeak(ctx context.Context, roomID string) error
	SetMuted(ctx context.Context, roomID string, muted bool) error
	Invite(ctx context.Context, roomID, userID string) error
	CloseStale(ctx context.Context, idleMinutes int) (int, error)
}

// Notifier menyiarkan perubahan keadaan room ke peserta yang terhubung.
//
// Tanpa ini, peserta tidak pernah tahu room sudah berakhir atau ada yang
// mengangkat tangan — mereka harus menebak lewat polling, dan sempat terjadi
// peserta masih mengira berada di dalam room yang sudah ditutup.
type Notifier interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// Nama event yang disiarkan ke topik room:<id>.
const (
	EventRoomEnded        = "room.ended"
	EventRoomParticipants = "room.participants"
	EventSpeakRequest     = "room.speak_request"
	EventRoleChanged      = "room.role_changed"
	EventJoinDecided      = "room.join_decided"
)

// EventRoomCreated disiarkan ke feed global rooms:all saat room PUBLIK dibuat,
// supaya Voice Hub orang lain menampilkannya tanpa polling.
const EventRoomCreated = "room.created"

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
	repo     Repository
	issuer   TokenIssuer
	notifier Notifier
	// avatarURL mengubah storage key jadi URL siap-render, supaya siaran daftar
	// peserta lewat WS membawa avatar_url yang sama seperti respons HTTP.
	avatarURL func(string) string
}

// NewService merangkai service.
func NewService(repo Repository, issuer TokenIssuer, notifier Notifier, avatarURL func(string) string) *Service {
	if avatarURL == nil {
		avatarURL = func(string) string { return "" }
	}
	return &Service{repo: repo, issuer: issuer, notifier: notifier, avatarURL: avatarURL}
}

// topicFor menyusun nama kanal siaran sebuah room.
func topicFor(roomID string) string { return "room:" + roomID }

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

	// Pembuat sudah tercatat sebagai host dan tidak dibisukan, jadi hitungan
	// ini benar sejak awal — daftar room tidak akan menampilkan "0 peserta"
	// untuk room yang baru saja dibuka.
	r.ParticipantCount = 1
	r.SpeakerCount = 1
	r.MaxParticipants = defaultMaxParticipants
	return r, nil
}

// CreateAndJoin membuat room lalu langsung menerbitkan token untuk hostnya.
//
// Menggabungkan keduanya menghemat satu perjalanan bolak-balik, tetapi alasan
// sebenarnya lebih penting: kalau klien harus memanggil join secara terpisah,
// ada jeda saat room sudah tampil di daftar orang lain sementara pembuatnya
// sendiri belum tersambung ke audio.
func (s *Service) CreateAndJoin(ctx context.Context, in CreateInput, identity string) (Room, Join, error) {
	created, err := s.Create(ctx, in)
	if err != nil {
		return Room{}, Join{}, err
	}

	joined, err := s.Join(ctx, created.ID, in.HostID, identity)
	if err != nil {
		return created, Join{}, err
	}

	// Umumkan ke feed global HANYA kalau publik: room followers/invite_only tak
	// boleh bocor ke orang yang tak berhak masuk. Best effort — kegagalan siaran
	// tidak menggagalkan pembuatan room.
	if created.Visibility == VisibilityPublic && s.notifier != nil {
		_ = s.notifier.Publish(ctx, topic.RoomsFeed(), EventRoomCreated, map[string]any{
			"room_id":           created.ID,
			"title":             created.Title,
			"host_id":           created.HostID,
			"host_name":         created.HostName,
			"participant_count": 1,
			"visibility":        string(created.Visibility),
		})
	}
	return created, joined, nil
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

	status, role, sfuRoomID, err := s.repo.Join(ctx, roomID)
	if err != nil {
		return Join{}, err
	}

	// Menunggu persetujuan: tidak ada peran, tidak ada token.
	if status == JoinStatusPending {
		return Join{RoomID: roomID, Status: status}, nil
	}

	if sfuRoomID == "" {
		sfuRoomID = roomID
	}

	result := Join{
		RoomID:     roomID,
		Status:     JoinStatusJoined,
		Role:       role,
		SFURoomID:  sfuRoomID,
		CanPublish: role.CanPublish(),
	}

	// Beri tahu peserta yang SUDAH di dalam bahwa ada yang baru masuk. Tanpa ini,
	// daftar peserta di perangkat mereka basi sampai polling berikutnya — orang
	// yang baru gabung "belum muncul" di layar teman-temannya.
	s.notifyParticipants(ctx, roomID)

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

// End menutup room atas permintaan host, tanpa harus keluar lebih dulu.
func (s *Service) End(ctx context.Context, roomID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}

	if err := s.repo.End(ctx, roomID); err != nil {
		return err
	}

	s.notify(ctx, roomID, EventRoomEnded, map[string]any{
		"room_id": roomID,
		"reason":  "host_ended",
	})
	return nil
}

// CancelSpeakRequest menurunkan tangan yang sudah diangkat.
func (s *Service) CancelSpeakRequest(ctx context.Context, roomID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}

	if err := s.repo.CancelSpeakRequest(ctx, roomID); err != nil {
		return err
	}
	s.notifyParticipants(ctx, roomID)
	return nil
}

// JoinRequests mengembalikan permintaan masuk yang menunggu keputusan.
func (s *Service) JoinRequests(ctx context.Context, roomID string) ([]SpeakRequest, error) {
	if roomID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListJoinRequests(ctx, roomID)
}

// DecideJoinRequest menyetujui atau menolak permintaan masuk.
//
// Yang disetujui harus memanggil join lagi untuk mendapat token SFU — pada saat
// keputusan dibuat, ia belum tentu masih menunggu di layar.
func (s *Service) DecideJoinRequest(ctx context.Context, roomID, userID string, approve bool) error {
	if roomID == "" || userID == "" {
		return ErrInvalidInput
	}

	if err := s.repo.DecideJoinRequest(ctx, roomID, userID, approve); err != nil {
		return err
	}

	s.notify(ctx, roomID, EventJoinDecided, map[string]any{
		"room_id":  roomID,
		"user_id":  userID,
		"approved": approve,
	})
	return nil
}

// Leave mengeluarkan pemanggil. Kalau ia host, room ikut berakhir dan seluruh
// peserta lain diberi tahu.
func (s *Service) Leave(ctx context.Context, roomID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}

	ended, err := s.repo.Leave(ctx, roomID)
	if err != nil {
		return err
	}

	if ended {
		// Tanpa siaran ini, peserta yang tersisa tetap menampilkan layar room
		// dan mengira masih terhubung — padahal room-nya sudah ditutup.
		s.notify(ctx, roomID, EventRoomEnded, map[string]any{
			"room_id": roomID,
			"reason":  "host_left",
		})
		return nil
	}

	s.notifyParticipants(ctx, roomID)
	return nil
}

// Participants mengembalikan daftar peserta aktif, host lebih dulu.
func (s *Service) Participants(ctx context.Context, roomID string) ([]Participant, error) {
	if roomID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListParticipants(ctx, roomID)
}

// SpeakRequests mengembalikan permintaan bicara yang menunggu keputusan.
// Hanya host dan moderator yang boleh membacanya.
func (s *Service) SpeakRequests(ctx context.Context, roomID string) ([]SpeakRequest, error) {
	if roomID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListSpeakRequests(ctx, roomID)
}

// Invite menambahkan seseorang ke daftar undangan room invite_only.
func (s *Service) Invite(ctx context.Context, roomID, userID string) error {
	if roomID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.Invite(ctx, roomID, userID)
}

// CloseStale menutup room yang ditinggalkan tanpa sempat diakhiri.
//
// Dijalankan berkala oleh internal/app. Tanpa ini, room yang host-nya kehabisan
// baterai atau kehilangan jaringan menetap sebagai "live" selamanya dan
// menumpuk di daftar.
func (s *Service) CloseStale(ctx context.Context, idleMinutes int) (int, error) {
	if idleMinutes <= 0 {
		idleMinutes = 30
	}
	return s.repo.CloseStale(ctx, idleMinutes)
}

func (s *Service) notify(ctx context.Context, roomID, event string, payload any) {
	if s.notifier == nil {
		return
	}
	// Kegagalan siaran tidak boleh membatalkan operasi yang sudah tersimpan.
	_ = s.notifier.Publish(ctx, topicFor(roomID), event, payload)
}

// notifyParticipants menyiarkan daftar peserta terbaru.
//
// Dikirim utuh, bukan sebagai delta, karena daftar room selalu kecil dan
// pengiriman utuh membuat klien tidak bisa kehilangan sinkronisasi setelah
// satu frame terlewat.
//
// Payload dibangun sebagai map ber-field snake_case DAN membawa avatar_url yang
// sudah di-resolve — PERSIS bentuk yang dikirim handler HTTP GET .../participants.
// Sebelumnya struct room.Participant disiarkan mentah, sehingga Go men-serialisasi
// nama field PascalCase (UserID, DisplayName, …) yang tidak bisa diurai klien
// (ia membaca user_id, display_name). Akibatnya SELURUH event peserta gagal diurai
// dan voice room hanya ter-update lewat polling — tampak "tidak live".
func (s *Service) notifyParticipants(ctx context.Context, roomID string) {
	if s.notifier == nil {
		return
	}
	people, err := s.repo.ListParticipants(ctx, roomID)
	if err != nil {
		return
	}
	dto := make([]map[string]any, len(people))
	for i, p := range people {
		dto[i] = map[string]any{
			"user_id":         p.UserID,
			"username":        p.Username,
			"display_name":    p.DisplayName,
			"avatar_url":      s.avatarURL(p.AvatarKey),
			"role":            string(p.Role),
			"is_muted":        p.IsMuted,
			"has_raised_hand": p.HasRaisedHand,
			"joined_at":       p.JoinedAt,
		}
	}
	s.notify(ctx, roomID, EventRoomParticipants, map[string]any{
		"room_id":      roomID,
		"participants": dto,
	})
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

	if err := s.repo.SetRole(ctx, roomID, targetID, role); err != nil {
		return err
	}

	// Yang dipromosikan harus tahu bahwa ia perlu meminta token SFU baru —
	// token lamanya diterbitkan dengan canPublish=false dan tidak akan bisa
	// menyalakan mikrofon meski tombolnya sudah muncul.
	s.notify(ctx, roomID, EventRoleChanged, map[string]any{
		"room_id":      roomID,
		"user_id":      targetID,
		"role":         string(role),
		"needs_rejoin": role.CanPublish(),
	})
	s.notifyParticipants(ctx, roomID)
	return nil
}

// RequestSpeak mengangkat tangan.
func (s *Service) RequestSpeak(ctx context.Context, roomID, userID string) error {
	if roomID == "" {
		return ErrInvalidInput
	}

	if err := s.repo.RequestSpeak(ctx, roomID); err != nil {
		return err
	}

	// Inilah yang membuat "menunggu izin" berarti sesuatu: tanpa siaran ini,
	// host tidak pernah tahu ada yang mengangkat tangan, dan permintaannya
	// menggantung selamanya.
	s.notify(ctx, roomID, EventSpeakRequest, map[string]any{
		"room_id": roomID,
		"user_id": userID,
	})
	s.notifyParticipants(ctx, roomID)
	return nil
}

// SetMuted mengubah status bisu diri sendiri.
func (s *Service) SetMuted(ctx context.Context, roomID string, muted bool) error {
	if roomID == "" {
		return ErrInvalidInput
	}

	if err := s.repo.SetMuted(ctx, roomID, muted); err != nil {
		return err
	}

	s.notifyParticipants(ctx, roomID)
	return nil
}
