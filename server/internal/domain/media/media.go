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
}

// Storage adalah port object storage.
type Storage interface {
	SignUpload(ctx context.Context, bucket, path string) (url, token string, err error)
	PublicURL(bucket, path string) string
}

// Service memuat alur bisnis media.
type Service struct {
	repo    Repository
	storage Storage
	bucket  string
}

// NewService merangkai service.
func NewService(repo Repository, storage Storage, bucket string) *Service {
	if bucket == "" {
		bucket = "media"
	}
	return &Service{repo: repo, storage: storage, bucket: bucket}
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

// PublicURL menyusun URL baca untuk sebuah storage key.
func (s *Service) PublicURL(storageKey string) string {
	if storageKey == "" {
		return ""
	}
	return s.storage.PublicURL(s.bucket, storageKey)
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
