package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
)

// Bagian ini melengkapi Chat handler dengan fitur ala WhatsApp: info grup,
// kelola anggota, keluar, ganti judul, peran, bisukan, dan reaksi.
//
// Method di-attach ke *Chat yang sama supaya semua endpoint percakapan berada
// di satu tempat; ChatService di chat.go sudah memuat kontraknya.

type conversationDetailDTO struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	MyRole      string    `json:"my_role"`
	IsMuted     bool      `json:"is_muted"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// GetConversation menangani GET /api/v1/conversations/{id}.
func (h *Chat) GetConversation(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetConversation(r.Context(), r.PathValue("id"), auth.UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.OK(w, conversationDetailDTO{
		ID: d.ID, Type: string(d.Type), Title: d.Title, Description: d.Description,
		AvatarURL: h.media.PublicURL(d.AvatarKey), CreatedBy: d.CreatedBy,
		MyRole: d.MyRole, IsMuted: d.IsMuted, MemberCount: d.MemberCount, CreatedAt: d.CreatedAt,
	})
}

type memberDTO struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

// ListMembers menangani GET /api/v1/conversations/{id}/members.
func (h *Chat) ListMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.svc.Members(r.Context(), r.PathValue("id"), auth.UserID(r.Context()))
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	items := make([]memberDTO, 0, len(members))
	for _, m := range members {
		items = append(items, memberDTO{
			UserID: m.UserID, Username: m.Username, DisplayName: m.DisplayName,
			AvatarURL: h.media.PublicURL(m.AvatarKey), Role: m.Role, JoinedAt: m.JoinedAt,
		})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

type addMembersRequest struct {
	MemberIDs []string `json:"member_ids"`
}

// AddMembers menangani POST /api/v1/conversations/{id}/members.
func (h *Chat) AddMembers(w http.ResponseWriter, r *http.Request) {
	var req addMembersRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	added, err := h.svc.AddMembers(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), req.MemberIDs)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.OK(w, map[string]int{"added": added})
}

// RemoveMember menangani DELETE /api/v1/conversations/{id}/members/{user_id}.
func (h *Chat) RemoveMember(w http.ResponseWriter, r *http.Request) {
	err := h.svc.RemoveMember(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), r.PathValue("user_id"))
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// LeaveConversation menangani POST /api/v1/conversations/{id}/leave.
func (h *Chat) LeaveConversation(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Leave(r.Context(), r.PathValue("id"), auth.UserID(r.Context())); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type updateGroupRequest struct {
	Title         string  `json:"title,omitempty"`
	AvatarMediaID string  `json:"avatar_media_id,omitempty"`
	// Pointer: absent = jangan ubah deskripsi; "" = kosongkan.
	Description *string `json:"description,omitempty"`
}

// UpdateGroup menangani PATCH /api/v1/conversations/{id}.
func (h *Chat) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	var req updateGroupRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	err := h.svc.UpdateGroup(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), req.Title, req.AvatarMediaID, req.Description)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	h.GetConversation(w, r)
}

type setMemberRoleRequest struct {
	Role string `json:"role"`
}

// SetMemberRole menangani PATCH /api/v1/conversations/{id}/members/{user_id}.
func (h *Chat) SetMemberRole(w http.ResponseWriter, r *http.Request) {
	var req setMemberRoleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	err := h.svc.SetMemberRole(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), r.PathValue("user_id"), req.Role)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type muteRequest struct {
	// Aplikasi mengirim durasi dalam menit — inilah bentuk utama yang dipakai
	// klien: null/0 berarti bunyikan lagi, angka positif membisukan selama itu.
	DurationMinutes *int `json:"duration_minutes"`

	// Alternatif waktu absolut RFC3339 (null = bunyikan lagi). Disediakan bagi
	// klien yang lebih suka menghitung sendiri kapan bisu berakhir.
	MutedUntil *time.Time `json:"muted_until"`
}

// Mute menangani PUT /api/v1/conversations/{id}/mute.
//
// Menerima dua bentuk supaya cocok dengan aplikasi (duration_minutes) tanpa
// menutup pintu bagi klien yang mengirim waktu absolut (muted_until). Keduanya
// bermuara pada satu nilai: kapan bisu berakhir — atau nil untuk membunyikan.
func (h *Chat) Mute(w http.ResponseWriter, r *http.Request) {
	var req muteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	var until *time.Time
	switch {
	case req.DurationMinutes != nil && *req.DurationMinutes > 0:
		t := time.Now().UTC().Add(time.Duration(*req.DurationMinutes) * time.Minute)
		until = &t
	case req.MutedUntil != nil:
		until = req.MutedUntil
	}
	// Selain itu until tetap nil → bunyikan lagi.

	if err := h.svc.Mute(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), until); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type reactRequest struct {
	// Emoji kosong menghapus reaksi.
	Emoji string `json:"emoji"`
}

// React menangani PUT /api/v1/messages/{id}/reaction.
func (h *Chat) React(w http.ResponseWriter, r *http.Request) {
	var req reactRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if err := h.svc.React(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), req.Emoji); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type reactionDTO struct {
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	Emoji     string `json:"emoji"`
}

// Reactions menangani GET /api/v1/conversations/{id}/reactions?message_ids=a,b,c.
func (h *Chat) Reactions(w http.ResponseWriter, r *http.Request) {
	ids := splitParam(r.URL.Query().Get("message_ids"))
	if len(ids) == 0 {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "message_ids wajib diisi")
		return
	}
	reactions, err := h.svc.Reactions(r.Context(), auth.UserID(r.Context()), ids)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	items := make([]reactionDTO, 0, len(reactions))
	for _, x := range reactions {
		items = append(items, reactionDTO{MessageID: x.MessageID, UserID: x.UserID, Emoji: x.Emoji})
	}
	httpx.Page(w, items, pageMeta{Count: len(items)})
}

// splitParam memecah daftar id yang dipisah koma di query string.
func splitParam(s string) []string {
	out := make([]string, 0, 8)
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
