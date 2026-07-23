package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/media"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// MediaService adalah bagian domain media yang dipakai handler REST.
type MediaService interface {
	PrepareUpload(ctx context.Context, userID string, kind media.Kind, extension string) (media.Upload, error)
	Confirm(ctx context.Context, a media.Asset) error
	PublicURL(storageKey string) string
}

// Media menangani alur unggah media.
type Media struct {
	svc MediaService
}

// NewMedia membuat handler media.
func NewMedia(svc MediaService) *Media {
	return &Media{svc: svc}
}

type uploadURLRequest struct {
	Kind      string `json:"kind"`
	Extension string `json:"extension,omitempty"`
}

type uploadURLResponse struct {
	MediaID    string `json:"media_id"`
	Bucket     string `json:"bucket"`
	StorageKey string `json:"storage_key"`
	UploadURL  string `json:"upload_url"`
	Token      string `json:"token,omitempty"`
}

// PrepareUpload menangani POST /api/v1/media/upload-url.
//
// Langkah pertama dari tiga:
//
//  1. endpoint ini            → dapat media_id + upload_url
//  2. PUT byte ke upload_url  → langsung ke Supabase Storage, bukan ke sini
//  3. POST .../confirm        → metadata tercatat di database
//
// Byte media tidak pernah melewati server ini. Kalau ia jadi perantara, satu
// unggahan video menahan memori dan goroutine tanpa memberi nilai apa pun.
func (h *Media) PrepareUpload(w http.ResponseWriter, r *http.Request) {
	var req uploadURLRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	upload, err := h.svc.PrepareUpload(
		r.Context(),
		auth.UserID(r.Context()),
		media.Kind(req.Kind),
		req.Extension,
	)
	if err != nil {
		writeMediaError(w, r, err)
		return
	}

	httpx.OK(w, uploadURLResponse{
		MediaID:    upload.MediaID,
		Bucket:     upload.Bucket,
		StorageKey: upload.StorageKey,
		UploadURL:  upload.UploadURL,
		Token:      upload.Token,
	})
}

type confirmUploadRequest struct {
	Kind       string `json:"kind"`
	StorageKey string `json:"storage_key"`
	MimeType   string `json:"mime_type,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	DurationMs int    `json:"duration_ms,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

type mediaDTO struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Confirm menangani POST /api/v1/media/{id}/confirm.
//
// Dipanggil setelah unggahan selesai. Sebelum ini, media_id belum ada di
// database sama sekali — jadi story atau pesan yang menunjuk ke media yang
// belum dikonfirmasi akan ditolak.
func (h *Media) Confirm(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("id")
	if mediaID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id media tidak boleh kosong")
		return
	}

	var req confirmUploadRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	asset := media.Asset{
		ID:         mediaID,
		OwnerID:    auth.UserID(r.Context()),
		Kind:       media.Kind(req.Kind),
		StorageKey: req.StorageKey,
		MimeType:   req.MimeType,
		SizeBytes:  req.SizeBytes,
		DurationMs: req.DurationMs,
		Width:      req.Width,
		Height:     req.Height,
	}

	if err := h.svc.Confirm(r.Context(), asset); err != nil {
		writeMediaError(w, r, err)
		return
	}

	httpx.Created(w, mediaDTO{
		ID:  mediaID,
		URL: h.svc.PublicURL(req.StorageKey),
	})
}

func writeMediaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, media.ErrUnknownKind):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
			`kind harus salah satu dari: image, video, audio, voice_note`)

	case errors.Is(err, media.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	case errors.Is(err, media.ErrTooLarge):
		httpx.Fail(w, r, http.StatusRequestEntityTooLarge, httpx.CodeBadRequest,
			"berkas terlalu besar: maks 10MB gambar, 100MB video, 20MB audio, 16MB voice note")

	case errors.Is(err, media.ErrTooLong):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
			"durasi video terlalu panjang: maksimum 3 menit")

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
