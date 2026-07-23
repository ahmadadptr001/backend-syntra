package handler

import (
	"context"
	"errors"
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
	SFUReady() bool
}

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
