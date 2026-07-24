// Package presence melacak siapa yang sedang online.
//
// Aplikasi menampilkan tiga keadaan: titik hijau (online), "typing…", dan
// "last seen recently". Typing ditangani lapisan WebSocket sebagai siaran
// sesaat; dua sisanya ada di sini.
//
// Datanya TIDAK disimpan di Postgres. Presence berubah setiap kali seseorang
// membuka atau menutup aplikasi, dan riwayatnya tidak punya nilai apa pun —
// menuliskannya ke database berarti membebani tabel dengan update yang tidak
// pernah dibaca lagi. Tempatnya di Redis, dengan TTL sebagai mekanisme
// kedaluwarsa otomatis: kalau proses server mati mendadak, statusnya hilang
// sendiri alih-alih membuat semua orang tampak online selamanya.
package presence

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidInput = errors.New("presence: input tidak valid")

// MaxQuery membatasi jumlah pengguna yang boleh ditanyakan sekaligus.
const MaxQuery = 200

// Status adalah keadaan seorang pengguna.
type Status struct {
	UserID   string
	Online   bool
	LastSeen time.Time // nol kalau tidak diketahui
}

// Store adalah port penyimpanan efemeral. Diimplementasikan oleh
// internal/repository/redisstore.
type Store interface {
	SetOnline(ctx context.Context, userID string, ttl time.Duration) error
	SetOffline(ctx context.Context, userID string, at time.Time) error
	Query(ctx context.Context, userIDs []string) (map[string]Status, error)
}

// Visibility menjawab apakah seorang pengguna mengizinkan status online-nya
// terlihat. Diimplementasikan oleh repository yang membaca user_settings.
//
// Opsional: kalau nil, semua orang dianggap terlihat (fitur privasi mati tanpa
// mengganggu presence biasa).
type Visibility interface {
	IsVisible(ctx context.Context, userID string) (bool, error)
}

// Service memuat alur bisnis presence.
type Service struct {
	store      Store
	visibility Visibility
	ttl        time.Duration
}

// NewService merangkai service.
//
// ttl harus lebih besar dari interval ping WebSocket. Kalau lebih kecil,
// pengguna yang koneksinya sehat akan berkedip offline di antara dua ping.
// visibility boleh nil.
func NewService(store Store, ttl time.Duration, visibility Visibility) *Service {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &Service{store: store, visibility: visibility, ttl: ttl}
}

// Visible menjawab apakah status online pengguna boleh terlihat lawan bicara.
//
// Dipakai lapisan WebSocket saat koneksi terbentuk: kalau false, backend tidak
// mencatat pengguna online dan tidak menyiarkan perubahan statusnya. Gagal
// memeriksa dianggap terlihat — privasi tidak boleh diam-diam menyembunyikan
// orang gara-gara satu query gagal, dan sebaliknya membuka presence saat ragu
// lebih sesuai dengan perilaku bawaan (terlihat).
func (s *Service) Visible(ctx context.Context, userID string) (bool, error) {
	if s.visibility == nil || userID == "" {
		return true, nil
	}
	return s.visibility.IsVisible(ctx, userID)
}

// Online menandai pengguna sedang terhubung dan menyegarkan TTL-nya.
func (s *Service) Online(ctx context.Context, userID string) error {
	if userID == "" {
		return ErrInvalidInput
	}
	return s.store.SetOnline(ctx, userID, s.ttl)
}

// Offline menandai pengguna terputus dan mencatat waktu terakhir terlihat.
func (s *Service) Offline(ctx context.Context, userID string) error {
	if userID == "" {
		return ErrInvalidInput
	}
	return s.store.SetOffline(ctx, userID, time.Now().UTC())
}

// Query mengambil status sekumpulan pengguna sekaligus.
//
// Klien menanyakan seluruh lawan bicara di daftar chat dalam satu permintaan,
// bukan satu per satu — satu round-trip untuk seluruh layar.
func (s *Service) Query(ctx context.Context, userIDs []string) (map[string]Status, error) {
	if len(userIDs) == 0 {
		return map[string]Status{}, nil
	}
	if len(userIDs) > MaxQuery {
		userIDs = userIDs[:MaxQuery]
	}
	return s.store.Query(ctx, userIDs)
}
