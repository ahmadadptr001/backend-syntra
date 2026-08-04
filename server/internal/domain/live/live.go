// Package live memuat aturan bisnis siaran langsung (live streaming).
//
// Seperti package room, package ini TIDAK mengalirkan video. Video realtime
// dibawa SFU (LiveKit): host MENERBITKAN track kamera, penonton BERLANGGANAN.
// Yang dilakukan package ini: menjadi OTORITAS — menentukan siapa host (boleh
// publish) dan siapa penonton (subscribe saja), lalu menerbitkan token yang
// membuktikannya.
//
//	host  ──video (UDP/WebRTC)──► SFU ──video──► penonton
//	  │                            ▲
//	  └──token & peran (HTTPS)──► backend ini
//
// Beda dari voice room: satu live selalu punya SATU host publisher dan banyak
// penonton listener — tidak ada speaker/moderator/raise-hand, dan live selalu
// publik.
package live

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
	ErrNotFound         = errors.New("live: live tidak ditemukan")
	ErrInvalidInput     = errors.New("live: input tidak valid")
	ErrNotAllowed       = errors.New("live: tidak diizinkan")
	ErrInsufficientCoin = errors.New("live: koin tidak cukup")
)

// EventLiveGift disiarkan ke topik live:<id> saat ada yang mengirim GIF gift.
const EventLiveGift = "live.gift"

// MaxTitleLength membatasi panjang judul live.
const MaxTitleLength = 100

// Role menentukan hak menerbitkan video.
type Role string

const (
	RoleHost   Role = "host"
	RoleViewer Role = "viewer"
)

// CanPublish menandai peran yang boleh menerbitkan video. Hanya host.
func (r Role) CanPublish() bool { return r == RoleHost }

// Live adalah satu sesi siaran langsung.
type Live struct {
	ID           string
	HostID       string
	HostUsername string
	HostName     string
	// HostAvatarID berisi storage_key (bukan id media) supaya bisa langsung
	// di-resolve jadi URL.
	HostAvatarID string

	Title    string
	Category string

	ViewerCount int
	SFURoomID   string
	StartedAt   time.Time
}

// Join adalah hasil bergabung: peran, dan bekal untuk menyambung ke SFU.
type Join struct {
	LiveID string

	Role      Role
	SFURoomID string

	// Token dan URL inilah yang sebenarnya dipakai klien untuk terhubung ke
	// media server. Host memakainya untuk publish kamera; penonton untuk
	// subscribe track host.
	SFUToken string
	SFUURL   string

	CanPublish bool
}

// Gift adalah satu GIF/gift di katalog.
type Gift struct {
	ID    string
	Code  string
	Emoji string
	Name  string
	Cost  int
}

// GiftResult adalah hasil mengirim gift: detail gift, saldo baru pengirim, dan
// nama pengirim yang di-resolve server (untuk siaran).
type GiftResult struct {
	Emoji          string
	Name           string
	Cost           int
	Balance        int
	SenderUsername string
}

// Repository adalah port penyimpanan.
type Repository interface {
	Create(ctx context.Context, l Live) error
	List(ctx context.Context) ([]Live, error)
	Get(ctx context.Context, liveID string) (Live, error)
	Join(ctx context.Context, liveID string) (role Role, sfuRoomID string, err error)
	End(ctx context.Context, liveID string) error

	// Leave mengembalikan true kalau yang keluar adalah host, yang berarti live
	// ikut berakhir dan penonton yang tersisa harus diberi tahu.
	Leave(ctx context.Context, liveID string) (endedLive bool, err error)

	CloseStale(ctx context.Context, idleMinutes int) (int, error)

	// Koin & gift.
	GetWallet(ctx context.Context) (int, error)
	TopUp(ctx context.Context, amount int) (int, error)
	ListGifts(ctx context.Context) ([]Gift, error)
	SendGift(ctx context.Context, giftRowID, liveID, giftID string) (GiftResult, error)
}

// Notifier menyiarkan kejadian live ke penonton yang terhubung (mis. gift masuk).
type Notifier interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// TokenIssuer menerbitkan kredensial masuk ke media server.
//
// Interface, bukan tipe konkret, supaya SFU-nya bisa ditukar tanpa menyentuh
// domain — sama seperti voice room dan call.
type TokenIssuer interface {
	Issue(roomID, userID, identity string, canPublish bool) (token, url string, err error)
	Configured() bool
}

// Service memuat alur bisnis live.
type Service struct {
	repo     Repository
	issuer   TokenIssuer
	notifier Notifier
}

// NewService merangkai service.
func NewService(repo Repository, issuer TokenIssuer, notifier Notifier) *Service {
	return &Service{repo: repo, issuer: issuer, notifier: notifier}
}

// SFUReady menandai apakah media server sudah dikonfigurasi. Tanpa itu live
// tetap bisa dibuat dan didaftar, tapi tidak akan ada video.
func (s *Service) SFUReady() bool { return s.issuer.Configured() }

// CreateInput adalah permintaan membuat live.
type CreateInput struct {
	HostID   string
	Title    string
	Category string
}

// Create membuka live baru dengan pemanggil sebagai host.
func (s *Service) Create(ctx context.Context, in CreateInput) (Live, error) {
	in.Title = strings.TrimSpace(in.Title)

	switch {
	case in.HostID == "" || in.Title == "":
		return Live{}, ErrInvalidInput
	case utf8.RuneCountInString(in.Title) > MaxTitleLength:
		return Live{}, ErrInvalidInput
	}

	liveID := id.New()
	l := Live{
		ID:       liveID,
		HostID:   in.HostID,
		Title:    in.Title,
		Category: strings.TrimSpace(in.Category),

		// Id room di SFU sengaja sama dengan id live di sini — tidak ada tabel
		// pemetaan yang harus dijaga sinkron.
		SFURoomID: liveID,
		StartedAt: time.Now().UTC(),
	}

	if err := s.repo.Create(ctx, l); err != nil {
		return Live{}, err
	}
	return l, nil
}

// CreateAndJoin membuat live lalu langsung menerbitkan token untuk hostnya,
// supaya host bisa mulai menyiarkan kamera tanpa perlu memanggil join terpisah.
func (s *Service) CreateAndJoin(ctx context.Context, in CreateInput, identity string) (Live, Join, error) {
	created, err := s.Create(ctx, in)
	if err != nil {
		return Live{}, Join{}, err
	}

	joined, err := s.Join(ctx, created.ID, in.HostID, identity)
	if err != nil {
		return created, Join{}, err
	}
	return created, joined, nil
}

// List mengembalikan live yang sedang berlangsung dan boleh dilihat pemanggil.
func (s *Service) List(ctx context.Context) ([]Live, error) {
	return s.repo.List(ctx)
}

// Get mengembalikan satu live — dipakai penonton memeriksa apakah siaran masih
// hidup. ErrNotFound berarti siaran sudah berakhir.
func (s *Service) Get(ctx context.Context, liveID string) (Live, error) {
	if liveID == "" {
		return Live{}, ErrInvalidInput
	}
	return s.repo.Get(ctx, liveID)
}

// Join memasukkan pemanggil ke live dan menerbitkan token SFU.
func (s *Service) Join(ctx context.Context, liveID, userID, identity string) (Join, error) {
	if liveID == "" || userID == "" {
		return Join{}, ErrInvalidInput
	}

	role, sfuRoomID, err := s.repo.Join(ctx, liveID)
	if err != nil {
		return Join{}, err
	}
	if sfuRoomID == "" {
		sfuRoomID = liveID
	}

	result := Join{
		LiveID:     liveID,
		Role:       role,
		SFURoomID:  sfuRoomID,
		CanPublish: role.CanPublish(),
	}

	// Tanpa SFU terkonfigurasi, keanggotaan tetap tercatat tetapi token kosong.
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

// End menutup live atas permintaan host.
func (s *Service) End(ctx context.Context, liveID string) error {
	if liveID == "" {
		return ErrInvalidInput
	}
	return s.repo.End(ctx, liveID)
}

// Leave mengeluarkan pemanggil. Kalau ia host, live ikut berakhir.
func (s *Service) Leave(ctx context.Context, liveID string) error {
	if liveID == "" {
		return ErrInvalidInput
	}
	_, err := s.repo.Leave(ctx, liveID)
	return err
}

// CloseStale menutup live yang ditinggalkan tanpa sempat diakhiri.
func (s *Service) CloseStale(ctx context.Context, idleMinutes int) (int, error) {
	if idleMinutes <= 0 {
		idleMinutes = 5
	}
	return s.repo.CloseStale(ctx, idleMinutes)
}

// Wallet mengembalikan saldo koin pemanggil (membuat dompet bila belum ada).
func (s *Service) Wallet(ctx context.Context) (int, error) {
	return s.repo.GetWallet(ctx)
}

// TopUp menambah koin (placeholder tanpa pembayaran nyata) dan mengembalikan saldo baru.
func (s *Service) TopUp(ctx context.Context, amount int) (int, error) {
	if amount <= 0 {
		return 0, ErrInvalidInput
	}
	return s.repo.TopUp(ctx, amount)
}

// Gifts mengembalikan katalog GIF/gift yang aktif.
func (s *Service) Gifts(ctx context.Context) ([]Gift, error) {
	return s.repo.ListGifts(ctx)
}

// SendGift mengurangi koin pemanggil, mencatat gift, lalu MENYIARKAN-nya ke seluruh
// penonton live lewat topik live:<id> (event live.gift). Mengembalikan detail gift +
// saldo baru pengirim.
func (s *Service) SendGift(ctx context.Context, liveID, giftID, senderID string) (GiftResult, error) {
	if liveID == "" || giftID == "" {
		return GiftResult{}, ErrInvalidInput
	}

	res, err := s.repo.SendGift(ctx, id.New(), liveID, giftID)
	if err != nil {
		return GiftResult{}, err
	}

	// Siarkan ke penonton (best effort — kegagalan siaran tidak membatalkan gift
	// yang sudah tercatat & koin yang sudah terpotong).
	if s.notifier != nil {
		_ = s.notifier.Publish(ctx, topic.Live(liveID), EventLiveGift, map[string]any{
			"live_id":         liveID,
			"sender_id":       senderID,
			"sender_username": res.SenderUsername,
			"emoji":           res.Emoji,
			"name":            res.Name,
			"cost":            res.Cost,
		})
	}
	return res, nil
}
