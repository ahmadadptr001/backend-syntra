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
	CreateAndJoin(ctx context.Context, in room.CreateInput, identity string) (room.Room, room.Join, error)
	List(ctx context.Context) ([]room.Room, error)
	Join(ctx context.Context, roomID, userID, identity string) (room.Join, error)
	Leave(ctx context.Context, roomID string) error
	End(ctx context.Context, roomID string) error
	CancelSpeakRequest(ctx context.Context, roomID string) error
	JoinRequests(ctx context.Context, roomID string) ([]room.SpeakRequest, error)
	DecideJoinRequest(ctx context.Context, roomID, userID string, approve bool) error
	Participants(ctx context.Context, roomID string) ([]room.Participant, error)
	SpeakRequests(ctx context.Context, roomID string) ([]room.SpeakRequest, error)
	SetRole(ctx context.Context, roomID, targetID string, role room.Role) error
	RequestSpeak(ctx context.Context, roomID, userID string) error
	SetMuted(ctx context.Context, roomID string, muted bool) error
	Invite(ctx context.Context, roomID, userID string) error
	SFUReady() bool
}

// Room menangani endpoint voice room.
type Room struct {
	svc   RoomService
	media MediaURLResolver
}

// NewRoom membuat handler voice room.
func NewRoom(svc RoomService, media MediaURLResolver) *Room {
	return &Room{svc: svc, media: media}
}

type roomDTO struct {
	ID           string `json:"id"`
	HostID       string `json:"host_id"`
	HostUsername string `json:"host_username,omitempty"`
	HostName     string `json:"host_name,omitempty"`
	// URL siap pakai. HostCoverURL adalah background/cover profil host, dipakai
	// app sebagai latar kartu room.
	HostAvatarURL string `json:"host_avatar_url,omitempty"`
	HostCoverURL  string `json:"host_cover_url,omitempty"`

	Title      string `json:"title"`
	Topic      string `json:"topic,omitempty"`
	Visibility string `json:"visibility"`

	ParticipantCount int `json:"participant_count"`
	SpeakerCount     int `json:"speaker_count"`
	MaxParticipants  int `json:"max_participants"`

	StartedAt time.Time `json:"started_at"`
}

func (h *Room) toRoomDTO(r room.Room) roomDTO {
	return roomDTO{
		ID:               r.ID,
		HostID:           r.HostID,
		HostUsername:     r.HostUsername,
		HostName:         r.HostName,
		HostAvatarURL:    h.media.PublicURL(r.HostAvatarID),
		HostCoverURL:     h.media.PublicURL(r.HostCoverID),
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
		items = append(items, h.toRoomDTO(rm))
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

type createdRoomDTO struct {
	roomDTO

	// Join disertakan supaya pembuat room langsung tersambung ke audio tanpa
	// perlu memanggil /join secara terpisah. Tanpa ini ada jeda saat room
	// sudah muncul di daftar orang lain sementara pembuatnya belum terhubung.
	Join joinDTO `json:"join"`
}

// Create menangani POST /api/v1/rooms.
//
// Membuat room DAN langsung memasukkan pembuatnya sebagai host yang tidak
// dibisukan, lengkap dengan token SFU. Balasannya berisi seluruh data room,
// jadi klien bisa menampilkannya seketika tanpa memuat ulang daftar.
func (h *Room) Create(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	principal, _ := auth.FromContext(r.Context())

	created, joined, err := h.svc.CreateAndJoin(r.Context(), room.CreateInput{
		HostID:     principal.UserID,
		Title:      req.Title,
		Topic:      req.Topic,
		Visibility: room.Visibility(req.Visibility),
	}, principal.UserID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	httpx.Created(w, createdRoomDTO{
		roomDTO: h.toRoomDTO(created),
		Join: joinDTO{
			RoomID:     joined.RoomID,
			Status:     string(joined.Status),
			Role:       string(joined.Role),
			CanPublish: joined.CanPublish,
			SFURoomID:  joined.SFURoomID,
			SFUToken:   joined.SFUToken,
			SFUURL:     joined.SFUURL,
		},
	})
}

type joinDTO struct {
	RoomID string `json:"room_id"`

	// "joined" atau "pending". Pada "pending", permintaan masuk sedang
	// menunggu keputusan host dan sfu_token sengaja tidak diterbitkan —
	// kalau diterbitkan, ruang tunggu hanya jadi hiasan yang bisa dilewati.
	Status string `json:"status"`

	Role       string `json:"role,omitempty"`
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

	dto := joinDTO{
		RoomID:     joined.RoomID,
		Status:     string(joined.Status),
		Role:       string(joined.Role),
		CanPublish: joined.CanPublish,
		SFURoomID:  joined.SFURoomID,
		SFUToken:   joined.SFUToken,
		SFUURL:     joined.SFUURL,
	}

	// 202 memberi tahu klien bahwa permintaannya diterima tetapi belum selesai —
	// tepatnya keadaan "menunggu izin masuk".
	if joined.Status == room.JoinStatusPending {
		httpx.JSON(w, http.StatusAccepted, httpx.Response{Data: dto})
		return
	}

	httpx.OK(w, dto)
}

// End menangani POST /api/v1/rooms/{id}/end.
//
// Hanya host. Berbeda dari leave: host bisa menutup room tanpa harus keluar
// lebih dulu, dan seluruh peserta langsung dikeluarkan.
func (h *Room) End(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.End(r.Context(), r.PathValue("id")); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// CancelSpeakRequest menangani DELETE /api/v1/rooms/{id}/raise-hand.
//
// Bendera juga turun sendiri saat peran naik jadi speaker; endpoint ini untuk
// peminta yang berubah pikiran.
func (h *Room) CancelSpeakRequest(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.CancelSpeakRequest(r.Context(), r.PathValue("id")); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// JoinRequests menangani GET /api/v1/rooms/{id}/requests.
func (h *Room) JoinRequests(w http.ResponseWriter, r *http.Request) {
	reqs, err := h.svc.JoinRequests(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	items := make([]speakRequestDTO, 0, len(reqs))
	for _, q := range reqs {
		items = append(items, speakRequestDTO{
			UserID:      q.UserID,
			Username:    q.Username,
			DisplayName: q.DisplayName,
			AvatarURL:   h.media.PublicURL(q.AvatarKey),
			RequestedAt: q.RequestedAt,
		})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// ApproveJoin menangani POST /api/v1/rooms/{id}/requests/{user_id}/approve.
//
// Yang disetujui harus memanggil join lagi untuk mendapat token SFU — saat
// keputusan dibuat, ia belum tentu masih menunggu di layar.
func (h *Room) ApproveJoin(w http.ResponseWriter, r *http.Request) {
	h.decideJoin(w, r, true)
}

// RejectJoin menangani POST /api/v1/rooms/{id}/requests/{user_id}/reject.
func (h *Room) RejectJoin(w http.ResponseWriter, r *http.Request) {
	h.decideJoin(w, r, false)
}

func (h *Room) decideJoin(w http.ResponseWriter, r *http.Request, approve bool) {
	if err := h.svc.DecideJoinRequest(r.Context(), r.PathValue("id"), r.PathValue("user_id"), approve); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
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
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`

	// URL siap pakai, bukan id media. Klien tidak punya cara mengubah id
	// menjadi URL, jadi mengirim id saja membuat avatar mustahil dirender.
	AvatarURL string `json:"avatar_url,omitempty"`
	// CoverURL adalah background/cover profil peserta — latar ubin saat kamera mati.
	CoverURL string `json:"cover_url,omitempty"`

	Role          string    `json:"role"`
	IsMuted       bool      `json:"is_muted"`
	HasRaisedHand bool      `json:"has_raised_hand"`
	JoinedAt      time.Time `json:"joined_at"`
}

type speakRequestDTO struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
}

// SpeakRequests menangani GET /api/v1/rooms/{id}/speak-requests.
//
// Inilah yang membuat "angkat tangan" bermakna: tanpa endpoint ini, host tidak
// punya cara melihat siapa yang meminta izin, dan permintaan menggantung
// selamanya di sisi peminta.
func (h *Room) SpeakRequests(w http.ResponseWriter, r *http.Request) {
	reqs, err := h.svc.SpeakRequests(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, r, err)
		return
	}

	items := make([]speakRequestDTO, 0, len(reqs))
	for _, q := range reqs {
		items = append(items, speakRequestDTO{
			UserID:      q.UserID,
			Username:    q.Username,
			DisplayName: q.DisplayName,
			AvatarURL:   h.media.PublicURL(q.AvatarKey),
			RequestedAt: q.RequestedAt,
		})
	}

	httpx.Page(w, items, pageMeta{Count: len(items)})
}

type inviteRequest struct {
	UserID string `json:"user_id"`
}

// Invite menangani POST /api/v1/rooms/{id}/invite — untuk room invite_only.
func (h *Room) Invite(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	if err := h.svc.Invite(r.Context(), r.PathValue("id"), req.UserID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	httpx.NoContent(w)
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
			UserID:        p.UserID,
			Username:      p.Username,
			DisplayName:   p.DisplayName,
			AvatarURL:     h.media.PublicURL(p.AvatarKey),
			CoverURL:      h.media.PublicURL(p.CoverKey),
			Role:          string(p.Role),
			IsMuted:       p.IsMuted,
			HasRaisedHand: p.HasRaisedHand,
			JoinedAt:      p.JoinedAt,
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
	if err := h.svc.RequestSpeak(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
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
