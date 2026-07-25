// Package media mengurus siklus hidup berkas media.
//
// Alurnya tiga langkah, dan urutannya penting:
//
//  1. Klien meminta izin unggah  → server membuat id + path + signed URL
//  2. Klien mengunggah byte      → langsung ke object storage, bukan ke server ini
//  3. Klien mengonfirmasi        → server mencatat metadatanya di database
//
// Byte media tidak pernah melewati proses Go. Kalau ia jadi perantara, satu
// unggahan video 50 MB menahan memori dan goroutine tanpa memberi nilai apa
// pun — dan pada saat reels berjalan, itu berlipat ribuan kali.
package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
)

var (
	ErrInvalidInput = errors.New("media: input tidak valid")
	ErrUnknownKind  = errors.New("media: jenis media tidak dikenal")
	ErrTooLarge     = errors.New("media: berkas terlalu besar")
	ErrTooLong      = errors.New("media: durasi media terlalu panjang")
	ErrNotFound     = errors.New("media: tidak ditemukan")
	ErrNotOwner     = errors.New("media: bukan milik pengguna ini")
	ErrInUse        = errors.New("media: masih dipakai")
)

// Batas ukuran per jenis. Byte-nya memang tidak masuk Postgres — hanya storage
// key yang dicatat — tetapi tanpa batas, bucket object storage bisa membengkak
// tak terkendali begitu reels ramai. Batas ditegakkan saat konfirmasi, satu-
// satunya titik tempat server tahu ukuran akhir berkas.
const (
	MaxImageBytes     = 10 << 20  // 10 MB
	MaxVideoBytes     = 100 << 20 // 100 MB
	MaxAudioBytes     = 20 << 20  // 20 MB
	MaxVoiceNoteBytes = 16 << 20  // 16 MB

	// Durasi video dibatasi supaya berkas tetap kecil dan sejalan dengan format
	// short-video. Story dan reels sama-sama pendek; klip panjang tidak punya
	// tempat di sini.
	MaxVideoDurationMs = 3 * 60 * 1000 // 3 menit
)

// MaxBytes mengembalikan batas ukuran untuk sebuah jenis media.
func (k Kind) MaxBytes() int64 {
	switch k {
	case KindImage:
		return MaxImageBytes
	case KindVideo:
		return MaxVideoBytes
	case KindAudio:
		return MaxAudioBytes
	case KindVoiceNote:
		return MaxVoiceNoteBytes
	default:
		return 0
	}
}

// Kind mengikuti enum MEDIA_ASSETS.kind di docs/erd.md.
type Kind string

const (
	KindImage     Kind = "image"
	KindVideo     Kind = "video"
	KindAudio     Kind = "audio"
	KindVoiceNote Kind = "voice_note"
)

// Valid memeriksa apakah jenis media dikenal.
func (k Kind) Valid() bool {
	switch k {
	case KindImage, KindVideo, KindAudio, KindVoiceNote:
		return true
	default:
		return false
	}
}

// Asset adalah metadata sebuah berkas media.
type Asset struct {
	ID         string
	OwnerID    string
	Kind       Kind
	StorageKey string
	MimeType   string
	SizeBytes  int64
	DurationMs int
	Width      int
	Height     int
}

// Upload adalah izin unggah yang diberikan ke klien.
type Upload struct {
	MediaID    string
	Bucket     string
	StorageKey string
	UploadURL  string
	Token      string
}

// Repository adalah port penyimpanan metadata.
type Repository interface {
	Register(ctx context.Context, a Asset) error

	// Delete membuang baris metadata milik pengguna dan mengembalikan
	// storage_key-nya, supaya berkasnya bisa ikut dibuang. Menolak media milik
	// orang lain (ErrNotOwner) dan media yang masih ditunjuk sesuatu (ErrInUse).
	Delete(ctx context.Context, mediaID, ownerID string) (storageKey string, err error)
}

// Storage adalah port object storage.
type Storage interface {
	SignUpload(ctx context.Context, bucket, path string) (url, token string, err error)
	PublicURL(bucket, path string) string
	Delete(ctx context.Context, bucket, path string) error
}

// Service memuat alur bisnis media.
type Service struct {
	repo    Repository
	storage Storage
	bucket  string
	// cdnBase, kalau diisi, membuat PublicURL mengarah ke CDN (Cloudflare) alih-
	// alih langsung ke Supabase Storage — memangkas egress. Kosong = perilaku lama.
	cdnBase string
}

// NewService merangkai service.
func NewService(repo Repository, storage Storage, bucket, cdnBase string) *Service {
	if bucket == "" {
		bucket = "media"
	}
	return &Service{repo: repo, storage: storage, bucket: bucket, cdnBase: strings.TrimRight(cdnBase, "/")}
}

// PrepareUpload membuat id media dan izin unggah.
//
// Path disusun server, bukan klien. Kalau klien yang menentukan, ia bisa
// menulis ke path milik orang lain atau menimpa berkas yang sudah ada.
func (s *Service) PrepareUpload(ctx context.Context, userID string, kind Kind, extension string) (Upload, error) {
	if userID == "" {
		return Upload{}, ErrInvalidInput
	}
	if !kind.Valid() {
		return Upload{}, ErrUnknownKind
	}
	// Defense in depth: userID goes straight into the storage path. If a
	// misconfigured auth path ever leaked a JWT here (it once did — see
	// auth.DevVerifier), the key became video/<jwt>/... which Supabase can't even
	// serve (HTTP 400), and the file was effectively lost. Reject anything that
	// isn't a plain id so a bad key can never be created again.
	if !looksLikeUserID(userID) {
		return Upload{}, ErrInvalidInput
	}

	mediaID := id.New()
	key := fmt.Sprintf("%s/%s/%s%s", kind, userID, mediaID, sanitizeExtension(extension))

	url, token, err := s.storage.SignUpload(ctx, s.bucket, key)
	if err != nil {
		return Upload{}, err
	}

	return Upload{
		MediaID:    mediaID,
		Bucket:     s.bucket,
		StorageKey: key,
		UploadURL:  url,
		Token:      token,
	}, nil
}

// Confirm mencatat metadata setelah unggahan selesai.
func (s *Service) Confirm(ctx context.Context, a Asset) error {
	switch {
	case a.ID == "" || a.OwnerID == "" || a.StorageKey == "":
		return ErrInvalidInput
	case !a.Kind.Valid():
		return ErrUnknownKind
	case a.SizeBytes <= 0:
		return ErrInvalidInput
	case a.SizeBytes > a.Kind.MaxBytes():
		return ErrTooLarge
	case a.Kind == KindVideo && a.DurationMs > MaxVideoDurationMs:
		return ErrTooLong
	}

	if a.MimeType == "" {
		a.MimeType = "application/octet-stream"
	}

	return s.repo.Register(ctx, a)
}

// DeleteAsset menghapus media milik pengguna beserta berkasnya.
//
// Urutannya disengaja: baris metadata dulu, baru berkasnya. Kalau berkasnya
// yang lebih dulu dibuang lalu penghapusan baris gagal (misalnya media ternyata
// masih dipakai), tautan di tempat lain akan menunjuk berkas yang sudah lenyap.
// Dengan urutan ini, kegagalan sebelum baris hilang tidak merusak apa pun; dan
// begitu baris hilang, berkasnya memang sudah tidak ditunjuk siapa pun.
func (s *Service) DeleteAsset(ctx context.Context, mediaID, userID string) error {
	if mediaID == "" || userID == "" {
		return ErrInvalidInput
	}

	key, err := s.repo.Delete(ctx, mediaID, userID)
	if err != nil {
		return err
	}

	if key == "" {
		return nil
	}
	return s.storage.Delete(ctx, s.bucket, key)
}

// PublicURL menyusun URL baca untuk sebuah storage key.
func (s *Service) PublicURL(storageKey string) string {
	if storageKey == "" {
		return ""
	}
	// Lewat CDN kalau dikonfigurasi: Cloudflare men-cache tiap objek di edge,
	// jadi Supabase hanya membayar egress pada tarikan pertama (cache-fill).
	// Path meniru layout publik Supabase (<bucket>/<key>) supaya worker edge bisa
	// memetakan balik ke objek asalnya.
	if s.cdnBase != "" {
		return s.cdnBase + "/" + s.bucket + "/" + strings.TrimPrefix(storageKey, "/")
	}
	return s.storage.PublicURL(s.bucket, storageKey)
}

// looksLikeUserID menolak nilai yang jelas bukan id pengguna biasa — terutama
// JWT (mengandung titik, sangat panjang) — sebelum ia masuk ke path storage.
// UUID Supabase 36 karakter; batas 64 memberi kelonggaran tanpa meloloskan JWT.
func looksLikeUserID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r == '/' || r == '.' || r == ' ' {
			return false
		}
	}
	return true
}

// sanitizeExtension hanya meloloskan ekstensi sederhana. Nilai dari klien
// tidak pernah dipercaya untuk menyusun path.
func sanitizeExtension(ext string) string {
	ext = strings.TrimSpace(strings.ToLower(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) > 6 {
		return ""
	}
	for _, r := range ext[1:] {
		if r < 'a' || r > 'z' {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return ext
}
