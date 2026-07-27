package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/call"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// CallService adalah bagian domain call yang dipakai handler REST.
type CallService interface {
	Start(ctx context.Context, conversationID, userID, identity string, kind call.Kind) (call.Session, error)
	Answer(ctx context.Context, callID, userID, identity, conversationID string) (call.Session, error)
	Decline(ctx context.Context, callID, conversationID string) error
	Leave(ctx context.Context, callID, conversationID string) error
	Active(ctx context.Context, conversationID string) (*call.Active, error)
	Invite(ctx context.Context, callID, targetID, conversationID, kind, inviterID string) error
	Participants(ctx context.Context, callID string) ([]call.Participant, error)
	HandleSFUWebhook(ctx context.Context, authHeader string, body []byte) error
	SFUReady() bool
}

// maxWebhookBody membatasi ukuran body webhook. Payload LiveKit kecil (beberapa
// KB); batas ini mencegah kiriman raksasa menghabiskan memori.
const maxWebhookBody = 64 << 10

// Call menangani endpoint telepon & video call.
type Call struct {
	svc CallService
}

// NewCall membuat handler panggilan.
func NewCall(svc CallService) *Call {
	return &Call{svc: svc}
}

type sessionDTOCall struct {
	CallID    string `json:"call_id"`
	SFURoomID string `json:"sfu_room_id"`
	SFUToken  string `json:"sfu_token,omitempty"`
	SFUURL    string `json:"sfu_url,omitempty"`
	IsNew     bool   `json:"is_new,omitempty"`
}

type startCallRequest struct {
	ConversationID string `json:"conversation_id"`
	Kind           string `json:"kind"` // audio | video
}

// Start menangani POST /api/v1/calls.
//
// Sama seperti voice room, sfu_token inilah yang membuat suara/video benar-benar
// mengalir; token kosong berarti LiveKit belum dikonfigurasi.
func (h *Call) Start(w http.ResponseWriter, r *http.Request) {
	var req startCallRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	p, _ := auth.FromContext(r.Context())
	sess, err := h.svc.Start(r.Context(), req.ConversationID, p.UserID, p.UserID, call.Kind(req.Kind))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	httpx.Created(w, toCallSessionDTO(sess))
}

// Answer menangani POST /api/v1/calls/{id}/answer.
func (h *Call) Answer(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	sess, err := h.svc.Answer(r.Context(), r.PathValue("id"), p.UserID, p.UserID, r.URL.Query().Get("conversation_id"))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	httpx.OK(w, toCallSessionDTO(sess))
}

// Invite menangani POST /api/v1/calls/{id}/invite.
//
// Mengundang orang ke panggilan yang sedang berjalan — inilah yang membuat panggilan
// bisa disambung sampai lima orang. Batasnya ditegakkan di database.
func (h *Call) Invite(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var body struct {
		TargetID string `json:"target_id"`
		Kind     string `json:"kind"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if body.TargetID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "target_id wajib diisi")
		return
	}
	kind := body.Kind
	if kind == "" {
		kind = "audio"
	}
	err := h.svc.Invite(
		r.Context(), r.PathValue("id"), body.TargetID,
		r.URL.Query().Get("conversation_id"), kind, p.UserID,
	)
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type callParticipantDTO struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Joined      bool   `json:"joined"`
}

// Participants menangani GET /api/v1/calls/{id}/participants.
func (h *Call) Participants(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Participants(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	items := make([]callParticipantDTO, 0, len(list))
	for _, c := range list {
		items = append(items, callParticipantDTO{
			UserID: c.UserID, Username: c.Username,
			DisplayName: c.DisplayName, Joined: c.Joined,
		})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// Decline menangani POST /api/v1/calls/{id}/decline.
func (h *Call) Decline(w http.ResponseWriter, r *http.Request) {
	err := h.svc.Decline(r.Context(), r.PathValue("id"), r.URL.Query().Get("conversation_id"))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Leave menangani POST /api/v1/calls/{id}/leave.
func (h *Call) Leave(w http.ResponseWriter, r *http.Request) {
	err := h.svc.Leave(r.Context(), r.PathValue("id"), r.URL.Query().Get("conversation_id"))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type activeCallDTO struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	InitiatorID string    `json:"initiator_id"`
	StartedAt   time.Time `json:"started_at"`
}

// GetActive menangani GET /api/v1/conversations/{id}/call.
//
// Mengembalikan panggilan yang sedang berlangsung, atau data null kalau tidak
// ada — klien memakainya untuk menampilkan tombol "gabung panggilan".
func (h *Call) GetActive(w http.ResponseWriter, r *http.Request) {
	active, err := h.svc.Active(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCallError(w, r, err)
		return
	}
	if active == nil {
		httpx.OK(w, nil)
		return
	}
	httpx.OK(w, activeCallDTO{
		ID: active.ID, Kind: string(active.Kind), Status: active.Status,
		InitiatorID: active.InitiatorID, StartedAt: active.StartedAt,
	})
}

// Webhook menangani POST /api/v1/sfu/webhook.
//
// Endpoint ini PUBLIK — LiveKit Cloud harus bisa menjangkaunya, jadi ia berada
// di luar middleware auth. Keamanannya bukan JWT pengguna melainkan verifikasi
// tanda tangan di dalam service: hanya kiriman yang ditandatangani API secret
// LiveKit yang diproses.
//
// Selalu membalas 200 untuk kiriman yang sah walau eventnya diabaikan, supaya
// LiveKit tidak mengulang-ulang kirim. Hanya tanda tangan tidak sah yang 401.
func (h *Call) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "gagal membaca body")
		return
	}

	err = h.svc.HandleSFUWebhook(r.Context(), r.Header.Get("Authorization"), body)
	switch {
	case err == nil:
		httpx.NoContent(w)
	case errors.Is(err, call.ErrWebhookAuth):
		httpx.Fail(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, "tanda tangan webhook tidak sah")
	case errors.Is(err, call.ErrNoSFU):
		// Media server tidak dikonfigurasi — tidak ada yang bisa diverifikasi.
		httpx.Fail(w, r, http.StatusServiceUnavailable, httpx.CodeInternal, "media server tidak dikonfigurasi")
	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}

func toCallSessionDTO(s call.Session) sessionDTOCall {
	return sessionDTOCall{
		CallID: s.CallID, SFURoomID: s.SFURoomID,
		SFUToken: s.SFUToken, SFUURL: s.SFUURL, IsNew: s.IsNew,
	}
}

func writeCallError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, call.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "panggilan tidak ditemukan")
	case errors.Is(err, call.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "panggilan tidak diizinkan")
	case errors.Is(err, call.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "input tidak valid")
	default:
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
