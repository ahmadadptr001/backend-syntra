package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/room"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// RoomService adalah bagian domain room yang dipakai handler REST.
type RoomService interface {
	Create(ctx context.Context, in room.CreateInput) (room.Room, error)
	List(ctx context.Context) ([]room.Room, error)
	Join(ctx context.Context, roomID, userID, identity string) (room.Join, error)
	Leave(ctx context.Context, roomID string) error
	Participants(ctx context.Context, roomID string) ([]room.Participant, error)
	SetRole(ctx context.Context, roomID, targetID string, role room.Role) error
	RequestSpeak(ctx context.Context, roomID string) error
	SetMuted(ctx context.Context, roomID string, muted bool) error
	SFUReady() bool
}

// Room menangani endpoint voice room.
type Room struct {
	svc RoomService
}

// NewRoom membuat handler voice room.
func NewRoom(svc RoomService) *Room {
	return &Room{svc: svc}
}

type roomDTO struct {
	ID           string `json:"id"`
	HostID       string `json:"host_id"`
	HostUsername string `json:"host_username,omitempty"`
	HostName     string `json:"host_name,omitempty"`
	HostAvatarID string `json:"host_avatar_media_id,omitempty"`

	Title      string `json:"title"`
	Topic      string `json:"topic,omitempty"`
	Visibility string `json:"visibility"`

	ParticipantCount int `json:"participant_count"`
	SpeakerCount     int `json:"speaker_count"`
	MaxParticipants  int `json:"max_participants"`

	StartedAt time.Time `json:"started_at"`
}

func toRoomDTO(r room.Room) roomDTO {
	return roomDTO{
		ID:               r.ID,
		HostID:           r.HostID,
		HostUsername:     r.HostUsername,
		HostName:         r.HostName,
		HostAvatarID:     r.HostAvatarID,
		Title:            r.Title,
		Topic:            r.Topic,
		Visibility:       string(r.Visibility),
		ParticipantCount: r.ParticipantCount,
		SpeakerCount:     r.SpeakerCount,
		MaxParticipants:  r.MaxParticipants,
		StartedAt:        r.StartedAt,
	}
}

// List menangani GET /api/v1/rooms — daftar room yang sedang berlangsung.
func (h *Room) List(w http.ResponseWriter, r *http.Request) {
	rooms, err := h.svc.List(r.Context())
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	items := make([]roomDTO, 0, len(rooms))
	for _, rm := range rooms {
		items = append(items, toRoomDTO(rm))
	}

	// sfu_ready memberi tahu klien lebih awal bahwa room bisa dibuat dan
	// didaftar, tetapi tidak akan ada suara sampai media server dikonfigurasi.
	httpx.Page(w, items, map[string]any{
		"count":     len(items),
		"sfu_ready": h.svc.SFUReady(),
	})
}

type createRoomRequest struct {
	Title      string `json:"title"`
	Topic      string `json:"topic,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// Create menangani POST /api/v1/rooms.
func (h *Room) Create(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	created, err := h.svc.Create(r.Context(), room.CreateInput{
		HostID:     auth.UserID(r.Context()),
		Title:      req.Title,
		Topic:      req.Topic,
		Visibility: room.Visibility(req.Visibility),
	})
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	httpx.Created(w, toRoomDTO(created))
}

type joinDTO struct {
	RoomID     string `json:"room_id"`
	Role       string `json:"role"`
	CanPublish bool   `json:"can_publish"`

	// Dua field inilah yang membuat suara benar-benar terdengar. Klien
	// menyambung ke sfu_url memakai sfu_token lewat SDK LiveKit; audio mengalir
	// langsung antara klien dan media server, tidak lewat backend ini.
	//
	// Kalau sfu_token kosong, berarti media server belum dikonfigurasi:
	// keanggotaan tetap tercatat, tetapi tidak akan ada suara.
	SFURoomID string `json:"sfu_room_id"`
	SFUToken  string `json:"sfu_token,omitempty"`
	SFUURL    string `json:"sfu_url,omitempty"`
}

// Join menangani POST /api/v1/rooms/{id}/join.
func (h *Room) Join(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id room tidak boleh kosong")
		return
	}

	principal, _ := auth.FromContext(r.Context())

	joined, err := h.svc.Join(r.Context(), roomID, principal.UserID, principal.UserID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	httpx.OK(w, joinDTO{
		RoomID:     joined.RoomID,
		Role:       string(joined.Role),
		CanPublish: joined.CanPublish,
		SFURoomID:  joined.SFURoomID,
		SFUToken:   joined.SFUToken,
		SFUURL:     joined.SFUURL,
	})
}

// Leave menangani POST /api/v1/rooms/{id}/leave.
func (h *Room) Leave(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Leave(r.Context(), r.PathValue("id")); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type participantDTO struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarID    string    `json:"avatar_media_id,omitempty"`
	Role        string    `json:"role"`
	IsMuted     bool      `json:"is_muted"`
	JoinedAt    time.Time `json:"joined_at"`
}

// Participants menangani GET /api/v1/rooms/{id}/participants.
func (h *Room) Participants(w http.ResponseWriter, r *http.Request) {
	people, err := h.svc.Participants(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	items := make([]participantDTO, 0, len(people))
	for _, p := range people {
		items = append(items, participantDTO{
			UserID:      p.UserID,
			Username:    p.Username,
			DisplayName: p.DisplayName,
			AvatarID:    p.AvatarID,
			Role:        string(p.Role),
			IsMuted:     p.IsMuted,
			JoinedAt:    p.JoinedAt,
		})
	}

	httpx.Page(w, items, pageMeta{Count: len(items)})
}

type setRoleRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// SetRole menangani PATCH /api/v1/rooms/{id}/participants.
//
// Setelah peran naik menjadi speaker, klien yang bersangkutan **harus memanggil
// Join lagi** untuk mendapat token SFU baru — token lama diterbitkan dengan
// canPublish=false dan tidak akan bisa menyalakan mikrofon.
func (h *Room) SetRole(w http.ResponseWriter, r *http.Request) {
	var req setRoleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	if err := h.svc.SetRole(r.Context(), r.PathValue("id"), req.UserID, room.Role(req.Role)); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// RequestSpeak menangani POST /api/v1/rooms/{id}/raise-hand.
func (h *Room) RequestSpeak(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RequestSpeak(r.Context(), r.PathValue("id")); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type mutedRequest struct {
	Muted bool `json:"muted"`
}

// SetMuted menangani PATCH /api/v1/rooms/{id}/mute.
//
// Ini hanya mencatat status untuk ditampilkan ke peserta lain. Membisukan
// mikrofon yang sebenarnya dilakukan SDK LiveKit di sisi klien — keduanya
// harus dipanggil bersamaan supaya tampilan tidak berbohong.
func (h *Room) SetMuted(w http.ResponseWriter, r *http.Request) {
	var req mutedRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	if err := h.svc.SetMuted(r.Context(), r.PathValue("id"), req.Muted); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func writeRoomError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, room.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "room tidak ditemukan")

	case errors.Is(err, room.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "tidak diizinkan")

	case errors.Is(err, room.ErrFull):
		httpx.Fail(w, r, http.StatusConflict, httpx.CodeConflict, "room sudah penuh")

	case errors.Is(err, room.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	case errors.Is(err, room.ErrNoSFU):
		httpx.Fail(w, r, http.StatusServiceUnavailable, httpx.CodeInternal,
			"media server belum dikonfigurasi")

	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
