// Package supabase mengimplementasikan port repository lewat API Supabase.
//
// Seluruh operasi dijalankan sebagai pemanggil aslinya: JWT pengguna diambil
// dari konteks dan diteruskan pada setiap permintaan. Dengan begitu RLS di
// sisi database ikut menegakkan aturan yang sama dengan yang ditegakkan
// domain — dua lapis pertahanan, bukan satu.
//
// Query yang menyentuh lebih dari satu tabel dijalankan lewat fungsi database
// (RPC), bukan dirangkai dari beberapa permintaan HTTP. Alasannya bukan
// selera: PostgREST tidak punya transaksi lintas-permintaan, jadi merangkai
// tiga panggilan HTTP untuk satu operasi logis berarti menerima kemungkinan
// keadaan setengah jadi setiap kali salah satunya gagal.
package supabase

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/chat"
	sb "github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
)

// Kegagalan yang berasal dari lapisan ini sendiri, bukan dari Supabase.
var (
	ErrMissingToken     = errors.New("supabase: JWT pengguna tidak ada di konteks")
	ErrIdentityMismatch = errors.New("supabase: identitas pemanggil tidak cocok dengan pengguna yang diminta")
)

// SQLSTATE yang dipakai fungsi database untuk melaporkan kegagalan domain.
// Nilai ini harus sama persis dengan yang di-RAISE pada berkas migrasi.
const (
	sqlstateNotMember       = "42501" // insufficient_privilege
	sqlstateNotFound        = "P0002" // no_data_found
	sqlstateInvalidData     = "22023" // invalid_parameter_value
	sqlstateUniqueViolation = "23505" // unique_violation
)

// ChatRepository memenuhi kontrak chat.Repository.
type ChatRepository struct {
	client *sb.Client
}

// NewChatRepository membuat repository chat.
func NewChatRepository(client *sb.Client) *ChatRepository {
	return &ChatRepository{client: client}
}

// Pastikan kontrak terpenuhi saat kompilasi, bukan saat runtime.
var _ chat.Repository = (*ChatRepository)(nil)

// conversationRow memetakan kolom yang dikembalikan fungsi list_conversations.
// Nama tag JSON harus sama persis dengan nama kolom di RETURNS TABLE.
type conversationRow struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	Title           string  `json:"title"`
	AvatarMediaID   *string `json:"avatar_media_id"`
	CounterpartID   *string `json:"counterpart_id"`
	CounterpartUser *string `json:"counterpart_username"`
	CounterpartRead *string `json:"counterpart_last_read"`
	UnreadCount     int     `json:"unread_count"`
	LastMessagePrev string  `json:"last_message_preview"`
	LastMessageType string  `json:"last_message_type"`

	// Pointer karena kolom ini NULL pada percakapan yang belum berisi pesan.
	LastMessageSender *string `json:"last_message_sender"`

	LastMessageAt time.Time `json:"last_message_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListConversations memanggil fungsi list_conversations.
func (r *ChatRepository) ListConversations(ctx context.Context, userID string, limit int, before time.Time) ([]chat.Conversation, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}

	args := map[string]any{
		"p_before": before.UTC(),
		"p_limit":  limit,
	}

	var rows []conversationRow
	if err := r.client.RPC(ctx, "list_conversations", args, &rows, actor); err != nil {
		return nil, translate(err)
	}

	out := make([]chat.Conversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.Conversation{
			ID:                    row.ID,
			Type:                  chat.ConversationType(row.Type),
			Title:                 row.Title,
			AvatarMediaID:         deref(row.AvatarMediaID),
			CounterpartID:         deref(row.CounterpartID),
			CounterpartUsername:   deref(row.CounterpartUser),
			CounterpartLastReadID: deref(row.CounterpartRead),
			UnreadCount:           row.UnreadCount,
			LastMessagePreview:    row.LastMessagePrev,
			LastMessageType:       chat.MessageType(row.LastMessageType),
			LastMessageSender:     deref(row.LastMessageSender),
			LastMessageAt:         row.LastMessageAt,
			CreatedAt:             row.CreatedAt,
		})
	}
	return out, nil
}

// messageRow memetakan kolom yang dikembalikan fungsi get_messages.
type messageRow struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	SenderID       *string    `json:"sender_id"`
	Type           string     `json:"type"`
	Body           string     `json:"body"`
	ReplyToID      *string    `json:"reply_to_message_id"`
	CreatedAt      time.Time  `json:"created_at"`
	EditedAt       *time.Time `json:"edited_at"`
	IsDeleted      bool       `json:"is_deleted"`
	Attachments    *string    `json:"attachments"`
}

// ListMessages memanggil fungsi get_messages.
func (r *ChatRepository) ListMessages(ctx context.Context, conversationID, userID, before string, limit int) ([]chat.Message, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}

	args := map[string]any{
		"p_conversation": conversationID,
		"p_before":       nullIfEmpty(before),
		"p_limit":        limit,
	}

	var rows []messageRow
	if err := r.client.RPC(ctx, "get_messages", args, &rows, actor); err != nil {
		return nil, translate(err)
	}

	out := make([]chat.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.Message{
			ID:             row.ID,
			ConversationID: row.ConversationID,
			SenderID:       deref(row.SenderID),
			Type:           chat.MessageType(row.Type),
			Body:           row.Body,
			ReplyToID:      deref(row.ReplyToID),
			CreatedAt:      row.CreatedAt,
			EditedAt:       row.EditedAt,
			IsDeleted:      row.IsDeleted,
			AttachmentKeys: splitCSV(deref(row.Attachments)),
		})
	}
	return out, nil
}

// CreateDirect memanggil fungsi create_direct_conversation, yang idempoten.
func (r *ChatRepository) CreateDirect(ctx context.Context, userID, otherID string) (string, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return "", err
	}

	var conversationID string
	if err := r.client.RPC(ctx, "create_direct_conversation",
		map[string]any{"p_other": otherID}, &conversationID, actor); err != nil {
		return "", translate(err)
	}
	return conversationID, nil
}

// CreateGroup memanggil fungsi create_group_conversation.
func (r *ChatRepository) CreateGroup(ctx context.Context, userID, title string, memberIDs []string) (string, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return "", err
	}

	if memberIDs == nil {
		memberIDs = []string{}
	}

	args := map[string]any{
		"p_title":   title,
		"p_members": memberIDs,
	}

	var conversationID string
	if err := r.client.RPC(ctx, "create_group_conversation", args, &conversationID, actor); err != nil {
		return "", translate(err)
	}
	return conversationID, nil
}

// DeleteMessage memanggil fungsi delete_message.
func (r *ChatRepository) DeleteMessage(ctx context.Context, messageID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "delete_message",
		map[string]any{"p_message": messageID}, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

// ClearConversation memanggil fungsi clear_conversation.
func (r *ChatRepository) ClearConversation(ctx context.Context, conversationID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "clear_conversation",
		map[string]any{"p_conversation": conversationID}, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

// IsMember memanggil fungsi is_conversation_member.
func (r *ChatRepository) IsMember(ctx context.Context, conversationID, userID string) (bool, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return false, err
	}

	var member bool
	if err := r.client.RPC(ctx, "is_conversation_member",
		map[string]any{"p_conversation": conversationID}, &member, actor); err != nil {
		return false, translate(err)
	}
	return member, nil
}

// IsRoomParticipant memeriksa apakah pengguna sedang berada di sebuah voice room.
//
// Ada di ChatRepository — bukan RoomRepository — karena lapisan WebSocket
// mengotorisasi seluruh langganan topik lewat satu interface. Alternatifnya
// adalah menyuntikkan dua repository ke Hub hanya untuk satu pemeriksaan.
func (r *ChatRepository) IsRoomParticipant(ctx context.Context, roomID, userID string) (bool, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return false, err
	}

	query := url.Values{}
	query.Set("select", "id")
	query.Set("room_id", "eq."+roomID)
	query.Set("user_id", "eq."+userID)
	query.Set("left_at", "is.null")
	query.Set("limit", "1")

	var rows []struct {
		ID string `json:"id"`
	}
	if err := r.client.Select(ctx, "room_participants", &rows, actor, sb.WithQuery(query)); err != nil {
		return false, translate(err)
	}
	return len(rows) > 0, nil
}

// InsertMessage memanggil fungsi send_message.
//
// Fungsi itu menyimpan pesan, memperbarui ringkasan percakapan, dan menaikkan
// unread anggota lain dalam satu transaksi. Id dan waktu dibuat di Go lalu
// dikirim ke sana, supaya nilai yang sudah dikembalikan domain ke pengirim
// benar-benar identik dengan yang tersimpan.
func (r *ChatRepository) InsertMessage(ctx context.Context, msg chat.Message) error {
	actor, err := actorOption(ctx, msg.SenderID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_id":           msg.ID,
		"p_conversation": msg.ConversationID,
		"p_type":         string(msg.Type),
		"p_body":         nullIfEmpty(msg.Body),
		"p_reply_to":     nullIfEmpty(msg.ReplyToID),
		"p_created_at":   msg.CreatedAt.UTC(),
	}
	// p_media hanya disertakan saat ada lampiran. Tanpa ini, pesan teks biasa
	// memanggil send_message dengan 6 argumen — cocok dengan versi lama fungsi —
	// sehingga pengiriman pesan tetap jalan walau migrasi lampiran belum
	// dijalankan. Media adalah fitur baru yang memang butuh migrasi tersebut.
	if len(msg.MediaIDs) > 0 {
		args["p_media"] = msg.MediaIDs
	}

	if err := r.client.RPC(ctx, "send_message", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

// MarkRead memanggil fungsi mark_conversation_read.
func (r *ChatRepository) MarkRead(ctx context.Context, conversationID, userID, messageID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}

	args := map[string]any{
		"p_conversation": conversationID,
		"p_message":      messageID,
	}

	if err := r.client.RPC(ctx, "mark_conversation_read", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

// actorOption mengambil JWT pemanggil dari konteks.
//
// Pemeriksaan kecocokan identitas bukan formalitas: kalau service pernah
// dipanggil dengan userID selain pemilik token, permintaan itu akan berjalan
// dengan hak akses orang lain. Lebih baik gagal terang-terangan di sini
// daripada diam-diam menulis data atas nama pengguna yang salah.
func actorOption(ctx context.Context, userID string) (sb.Option, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.Token == "" {
		return nil, ErrMissingToken
	}
	if userID != "" && principal.UserID != userID {
		return nil, ErrIdentityMismatch
	}
	return sb.WithToken(principal.Token), nil
}

// --- Manajemen grup, reaksi, bisu (migrasi 14) ---

type convDetailRow struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	AvatarKey   string    `json:"avatar_key"`
	CreatedBy   *string   `json:"created_by"`
	MyRole      string    `json:"my_role"`
	IsMuted     bool      `json:"is_muted"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func (r *ChatRepository) GetConversation(ctx context.Context, conversationID, userID string) (chat.ConversationDetail, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return chat.ConversationDetail{}, err
	}
	var rows []convDetailRow
	if err := r.client.RPC(ctx, "get_conversation", map[string]any{"p_conversation": conversationID}, &rows, actor); err != nil {
		return chat.ConversationDetail{}, translate(err)
	}
	if len(rows) == 0 {
		return chat.ConversationDetail{}, chat.ErrNotFound
	}
	row := rows[0]
	return chat.ConversationDetail{
		ID: row.ID, Type: chat.ConversationType(row.Type), Title: row.Title,
		AvatarKey: row.AvatarKey, CreatedBy: deref(row.CreatedBy), MyRole: row.MyRole,
		IsMuted: row.IsMuted, MemberCount: row.MemberCount, CreatedAt: row.CreatedAt,
	}, nil
}

type memberRow struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarKey   string    `json:"avatar_key"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

func (r *ChatRepository) ListMembers(ctx context.Context, conversationID, userID string) ([]chat.Member, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	var rows []memberRow
	if err := r.client.RPC(ctx, "list_conversation_members", map[string]any{"p_conversation": conversationID}, &rows, actor); err != nil {
		return nil, translate(err)
	}
	out := make([]chat.Member, 0, len(rows))
	for _, m := range rows {
		out = append(out, chat.Member{
			UserID: m.UserID, Username: m.Username, DisplayName: m.DisplayName,
			AvatarKey: m.AvatarKey, Role: m.Role, JoinedAt: m.JoinedAt,
		})
	}
	return out, nil
}

func (r *ChatRepository) AddMembers(ctx context.Context, conversationID, userID string, memberIDs []string) (int, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return 0, err
	}
	if memberIDs == nil {
		memberIDs = []string{}
	}
	var added int
	if err := r.client.RPC(ctx, "add_group_members", map[string]any{"p_conversation": conversationID, "p_members": memberIDs}, &added, actor); err != nil {
		return 0, translate(err)
	}
	return added, nil
}

func (r *ChatRepository) RemoveMember(ctx context.Context, conversationID, userID, memberID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "remove_group_member", map[string]any{"p_conversation": conversationID, "p_member": memberID}, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

func (r *ChatRepository) Leave(ctx context.Context, conversationID, userID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	if err := r.client.RPC(ctx, "leave_conversation", map[string]any{"p_conversation": conversationID}, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

func (r *ChatRepository) UpdateGroup(ctx context.Context, conversationID, userID, title, avatarMediaID string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	args := map[string]any{"p_conversation": conversationID, "p_title": nullIfEmpty(title), "p_avatar_media": nullIfEmpty(avatarMediaID)}
	if err := r.client.RPC(ctx, "update_group", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

func (r *ChatRepository) SetMemberRole(ctx context.Context, conversationID, userID, memberID, role string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	args := map[string]any{"p_conversation": conversationID, "p_member": memberID, "p_role": role}
	if err := r.client.RPC(ctx, "set_member_role", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

func (r *ChatRepository) Mute(ctx context.Context, conversationID, userID string, until *time.Time) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	var untilArg any
	if until != nil {
		untilArg = until.UTC()
	}
	args := map[string]any{"p_conversation": conversationID, "p_until": untilArg}
	if err := r.client.RPC(ctx, "mute_conversation", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

func (r *ChatRepository) React(ctx context.Context, messageID, userID, emoji string) error {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return err
	}
	args := map[string]any{"p_message": messageID, "p_emoji": nullIfEmpty(emoji)}
	if err := r.client.RPC(ctx, "react_to_message", args, nil, actor); err != nil {
		return translate(err)
	}
	return nil
}

type reactionRow struct {
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	Emoji     string `json:"emoji"`
}

func (r *ChatRepository) ListReactions(ctx context.Context, userID string, messageIDs []string) ([]chat.Reaction, error) {
	actor, err := actorOption(ctx, userID)
	if err != nil {
		return nil, err
	}
	if messageIDs == nil {
		messageIDs = []string{}
	}
	var rows []reactionRow
	if err := r.client.RPC(ctx, "list_reactions", map[string]any{"p_message_ids": messageIDs}, &rows, actor); err != nil {
		return nil, translate(err)
	}
	out := make([]chat.Reaction, 0, len(rows))
	for _, x := range rows {
		out = append(out, chat.Reaction{MessageID: x.MessageID, UserID: x.UserID, Emoji: x.Emoji})
	}
	return out, nil
}

// splitCSV memecah storage key gabungan dari get_messages menjadi daftar.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// translate memetakan kegagalan Supabase ke error domain.
func translate(err error) error {
	var apiErr *sb.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	switch {
	case apiErr.Code == sqlstateNotMember, apiErr.IsDeniedByRLS():
		return chat.ErrNotMember
	case apiErr.Code == sqlstateNotFound, apiErr.IsNotFound():
		return chat.ErrNotFound
	case apiErr.Code == sqlstateInvalidData:
		return chat.ErrInvalidInput
	default:
		return err
	}
}

// deref membaca pointer string yang boleh NULL di sisi database.
// Domain memakai string kosong sebagai "tidak ada", supaya lapisan di atasnya
// tidak perlu berurusan dengan pointer nil sama sekali.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullIfEmpty mengubah string kosong menjadi NULL.
//
// Dibutuhkan karena kolom seperti reply_to_message_id bertipe uuid: string
// kosong bukan uuid yang sah dan akan ditolak Postgres, sedangkan NULL adalah
// yang sebenarnya dimaksud.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
