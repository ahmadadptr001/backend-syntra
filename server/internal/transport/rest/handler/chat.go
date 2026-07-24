package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/chat"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// ChatService adalah bagian domain chat yang dipakai handler REST.
type ChatService interface {
	ListConversations(ctx context.Context, userID string, limit int, before time.Time) ([]chat.Conversation, error)
	ListMessages(ctx context.Context, conversationID, userID, before string, limit int) ([]chat.Message, error)
	SendMessage(ctx context.Context, in chat.SendMessageInput) (chat.Message, error)
	StartDirect(ctx context.Context, userID, otherID string) (string, error)
	CreateGroup(ctx context.Context, userID, title string, memberIDs []string) (string, error)
	DeleteMessage(ctx context.Context, messageID, userID string) error
	EditMessage(ctx context.Context, messageID, userID, body string) error
	ClearConversation(ctx context.Context, conversationID, userID string) error
	DeleteConversation(ctx context.Context, conversationID, userID string) error

	GetConversation(ctx context.Context, conversationID, userID string) (chat.ConversationDetail, error)
	Members(ctx context.Context, conversationID, userID string) ([]chat.Member, error)
	AddMembers(ctx context.Context, conversationID, userID string, memberIDs []string) (int, error)
	RemoveMember(ctx context.Context, conversationID, userID, memberID string) error
	Leave(ctx context.Context, conversationID, userID string) error
	UpdateGroup(ctx context.Context, conversationID, userID, title, avatarMediaID string) error
	SetMemberRole(ctx context.Context, conversationID, userID, memberID, role string) error
	Mute(ctx context.Context, conversationID, userID string, until *time.Time) error
	React(ctx context.Context, messageID, userID, emoji string) error
	Reactions(ctx context.Context, userID string, messageIDs []string) ([]chat.Reaction, error)
}

// Chat menangani endpoint percakapan.
type Chat struct {
	svc   ChatService
	media MediaURLResolver
}

// NewChat membuat handler chat.
func NewChat(svc ChatService, media MediaURLResolver) *Chat {
	return &Chat{svc: svc, media: media}
}

type conversationDTO struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	AvatarMediaID string `json:"avatar_media_id,omitempty"`
	CounterpartID string `json:"counterpart_id,omitempty"`

	// Username lawan bicara (hanya percakapan direct). Dipakai layar panggilan
	// masuk & aksi laporkan/blokir dari daftar chat.
	CounterpartUsername string `json:"counterpart_username,omitempty"`

	// Bandingkan id pesan dengan nilai ini untuk menggambar ✓✓: id memakai
	// UUIDv7 yang terurut waktu, jadi `pesan.id <= counterpart_last_read_id`
	// berarti sudah dibaca. Hanya ada pada percakapan `direct`.
	CounterpartLastReadID string `json:"counterpart_last_read_id,omitempty"`

	UnreadCount     int    `json:"unread_count"`
	LastMessagePrev string `json:"last_message_preview"`
	LastMessageType string `json:"last_message_type,omitempty"`
	LastMessageBy   string `json:"last_message_sender_id,omitempty"`

	LastMessageAt time.Time `json:"last_message_at"`
	CreatedAt     time.Time `json:"created_at"`
}

type pageMeta struct {
	Count      int        `json:"count"`
	NextBefore *time.Time `json:"next_before,omitempty"`
}

// ListConversations menangani GET /api/v1/conversations.
func (h *Chat) ListConversations(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())

	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0 // service yang menentukan nilai bawaan dan batas atas
	}

	var before time.Time
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
				"parameter before harus berformat RFC3339, contoh: 2026-07-23T10:00:00Z")
			return
		}
	}

	conversations, err := h.svc.ListConversations(r.Context(), userID, limit, before)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	items := make([]conversationDTO, 0, len(conversations))
	for _, c := range conversations {
		items = append(items, conversationDTO{
			ID:                    c.ID,
			Type:                  string(c.Type),
			Title:                 c.Title,
			AvatarMediaID:         c.AvatarMediaID,
			CounterpartID:         c.CounterpartID,
			CounterpartUsername:   c.CounterpartUsername,
			CounterpartLastReadID: c.CounterpartLastReadID,
			UnreadCount:           c.UnreadCount,
			LastMessagePrev:       c.LastMessagePreview,
			LastMessageType:       string(c.LastMessageType),
			LastMessageBy:         c.LastMessageSender,
			LastMessageAt:         c.LastMessageAt,
			CreatedAt:             c.CreatedAt,
		})
	}

	meta := pageMeta{Count: len(items)}
	if n := len(items); n > 0 {
		meta.NextBefore = &items[n-1].LastMessageAt
	}

	httpx.Page(w, items, meta)
}

type sendMessageRequest struct {
	Type      string   `json:"type,omitempty"`
	Body      string   `json:"body,omitempty"`
	ReplyToID string   `json:"reply_to_id,omitempty"`
	MediaIDs  []string `json:"media_ids,omitempty"`
}

type messageDTO struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	SenderID       string     `json:"sender_id"`
	Type           string     `json:"type"`
	Body           string     `json:"body,omitempty"`
	ReplyToID      string     `json:"reply_to_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	EditedAt       *time.Time `json:"edited_at,omitempty"`
	IsDeleted      bool       `json:"is_deleted,omitempty"`
	Attachments    []string   `json:"attachments,omitempty"`
}

// toMessageDTO menerjemahkan pesan domain, memetakan storage key lampiran ke
// URL yang bisa dibuka klien. media diperlukan untuk itu; boleh nil untuk
// pesan tanpa lampiran.
func toMessageDTO(m chat.Message, media MediaURLResolver) messageDTO {
	var urls []string
	if len(m.AttachmentKeys) > 0 && media != nil {
		urls = make([]string, 0, len(m.AttachmentKeys))
		for _, k := range m.AttachmentKeys {
			urls = append(urls, media.PublicURL(k))
		}
	}
	return messageDTO{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		SenderID:       m.SenderID,
		Type:           string(m.Type),
		Body:           m.Body,
		ReplyToID:      m.ReplyToID,
		CreatedAt:      m.CreatedAt,
		EditedAt:       m.EditedAt,
		IsDeleted:      m.IsDeleted,
		Attachments:    urls,
	}
}

type messagePageMeta struct {
	Count      int    `json:"count"`
	NextBefore string `json:"next_before,omitempty"`
}

// ListMessages menangani GET /api/v1/conversations/{id}/messages.
//
// Terbaru dulu. Cursor `before` adalah id pesan, bukan timestamp: id memakai
// UUIDv7 yang terurut waktu, dan dua pesan bisa punya created_at identik —
// cursor waktu akan melewatkan salah satunya.
func (h *Chat) ListMessages(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	if conversationID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id percakapan tidak boleh kosong")
		return
	}

	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0 // service yang menentukan nilai bawaan dan batas atas
	}

	messages, err := h.svc.ListMessages(
		r.Context(),
		conversationID,
		auth.UserID(r.Context()),
		r.URL.Query().Get("before"),
		limit,
	)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	items := make([]messageDTO, 0, len(messages))
	for _, m := range messages {
		items = append(items, toMessageDTO(m, h.media))
	}

	meta := messagePageMeta{Count: len(items)}
	if n := len(items); n > 0 {
		meta.NextBefore = items[n-1].ID
	}

	httpx.Page(w, items, meta)
}

type createConversationRequest struct {
	Type      string   `json:"type"`
	UserID    string   `json:"user_id,omitempty"`
	Title     string   `json:"title,omitempty"`
	MemberIDs []string `json:"member_ids,omitempty"`
}

type createConversationResponse struct {
	ID string `json:"id"`
}

// CreateConversation menangani POST /api/v1/conversations.
//
// Untuk `type: "direct"` operasi ini **idempoten** — memanggilnya lagi dengan
// lawan bicara yang sama mengembalikan percakapan yang sudah ada. Klien boleh
// memanggilnya setiap kali pengguna menekan "kirim pesan" tanpa memeriksa dulu.
func (h *Chat) CreateConversation(w http.ResponseWriter, r *http.Request) {
	var req createConversationRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	userID := auth.UserID(r.Context())

	var (
		conversationID string
		err            error
	)

	switch chat.ConversationType(req.Type) {
	case chat.ConversationDirect:
		conversationID, err = h.svc.StartDirect(r.Context(), userID, req.UserID)

	case chat.ConversationGroup:
		conversationID, err = h.svc.CreateGroup(r.Context(), userID, req.Title, req.MemberIDs)

	default:
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
			`type harus "direct" atau "group"`)
		return
	}

	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	httpx.Created(w, createConversationResponse{ID: conversationID})
}

// SendMessage menangani POST /api/v1/conversations/{id}/messages.
//
// Endpoint ini sengaja disediakan meski pengiriman utama lewat WebSocket.
// Alasannya praktis: saat socket sedang terputus dan sedang reconnect, klien
// tetap bisa mengirim lewat HTTP. Keduanya memanggil service yang sama, jadi
// tidak ada aturan yang bisa menyimpang di antara dua jalur ini.
func (h *Chat) SendMessage(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	if conversationID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id percakapan tidak boleh kosong")
		return
	}

	var req sendMessageRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	msg, err := h.svc.SendMessage(r.Context(), chat.SendMessageInput{
		ConversationID: conversationID,
		SenderID:       auth.UserID(r.Context()),
		Type:           chat.MessageType(req.Type),
		Body:           req.Body,
		ReplyToID:      req.ReplyToID,
		MediaIDs:       req.MediaIDs,
	})
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	httpx.Created(w, toMessageDTO(msg, h.media))
}

// DeleteMessage menangani DELETE /api/v1/messages/{id}.
//
// Hanya pengirimnya. Soft delete — barisnya tetap muncul di riwayat dengan
// is_deleted=true supaya urutan pesan tidak berlubang bagi peserta lain.
func (h *Chat) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("id")
	if messageID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id pesan tidak boleh kosong")
		return
	}

	if err := h.svc.DeleteMessage(r.Context(), messageID, auth.UserID(r.Context())); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// DeleteMessageNested menangani DELETE /api/v1/conversations/{id}/messages/{message_id}.
//
// Bentuk bersarang yang RESTful, dipakai aplikasi. Setara dengan
// DELETE /api/v1/messages/{message_id} — keduanya menghapus satu pesan milik
// pengirimnya. id percakapan di path tidak dipakai; message_id sudah cukup
// menemukan pesannya, dan kepemilikan diperiksa di service.
func (h *Chat) DeleteMessageNested(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("message_id")
	if messageID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id pesan tidak boleh kosong")
		return
	}

	if err := h.svc.DeleteMessage(r.Context(), messageID, auth.UserID(r.Context())); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type editMessageRequest struct {
	Body string `json:"body"`
}

// EditMessage menangani PATCH /api/v1/messages/{id}.
//
// Hanya pengirimnya, hanya pesan teks. Menyiarkan message.updated supaya
// perangkat lain mengganti isinya di tempat, bukan menambah baris baru.
func (h *Chat) EditMessage(w http.ResponseWriter, r *http.Request) {
	h.editMessage(w, r, r.PathValue("id"))
}

// EditMessageNested menangani PATCH /api/v1/conversations/{id}/messages/{message_id}.
// Bentuk bersarang yang dipakai aplikasi; setara dengan yang di atas.
func (h *Chat) EditMessageNested(w http.ResponseWriter, r *http.Request) {
	h.editMessage(w, r, r.PathValue("message_id"))
}

func (h *Chat) editMessage(w http.ResponseWriter, r *http.Request, messageID string) {
	if messageID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id pesan tidak boleh kosong")
		return
	}

	var req editMessageRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	if err := h.svc.EditMessage(r.Context(), messageID, auth.UserID(r.Context()), req.Body); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ClearConversation menangani DELETE /api/v1/conversations/{id}/messages.
//
// Mengosongkan riwayat HANYA untuk pemanggil. Peserta lain tetap melihat
// percakapannya utuh — menghapus pesan dari layar orang lain bukan wewenang
// siapa pun di dalam percakapan.
func (h *Chat) ClearConversation(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	if conversationID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id percakapan tidak boleh kosong")
		return
	}

	if err := h.svc.ClearConversation(r.Context(), conversationID, auth.UserID(r.Context())); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// DeleteConversation menangani DELETE /api/v1/conversations/{id}.
//
// Menghapus seluruh obrolan dari daftar pemanggil — untuk chat pribadi maupun
// grup. Berbeda dari DELETE .../messages yang hanya mengosongkan pesan tapi
// percakapan tetap ada di daftar. Peserta lain tidak terpengaruh.
func (h *Chat) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	if conversationID == "" {
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "id percakapan tidak boleh kosong")
		return
	}

	if err := h.svc.DeleteConversation(r.Context(), conversationID, auth.UserID(r.Context())); err != nil {
		writeDomainError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// writeDomainError memetakan error domain ke status HTTP.
//
// Pemetaan hanya ada di sini. Domain tidak boleh tahu soal 403 atau 404,
// dan handler tidak boleh menebak-nebak dari teks error.
func writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, chat.ErrNotMember):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "kamu bukan anggota percakapan ini")

	case errors.Is(err, chat.ErrNotFound):
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "sumber daya tidak ditemukan")

	case errors.Is(err, chat.ErrNotAllowed):
		httpx.Fail(w, r, http.StatusForbidden, httpx.CodeForbidden, "percakapan tidak diizinkan")

	case errors.Is(err, chat.ErrNotGroup):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, "bukan percakapan grup")

	case errors.Is(err, chat.ErrEmptyBody),
		errors.Is(err, chat.ErrBodyTooLong),
		errors.Is(err, chat.ErrInvalidInput):
		httpx.Fail(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())

	default:
		// Isi error tak dikenal tidak pernah dikirim ke klien, tetapi harus
		// tetap sampai ke log — kalau tidak, yang tersisa cuma "status=500"
		// tanpa petunjuk apa pun.
		middleware.WithError(r, err)
		httpx.Fail(w, r, http.StatusInternalServerError, httpx.CodeInternal, "terjadi kesalahan internal")
	}
}
