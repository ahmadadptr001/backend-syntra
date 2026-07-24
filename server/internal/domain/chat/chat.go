// Package chat memuat aturan bisnis percakapan.
//
// Isi package domain: entitas, error domain, port (interface yang dibutuhkan
// domain), dan service. Yang TIDAK boleh ada di sini: SQL, tipe net/http, atau
// apa pun yang berbau Supabase. Domain mendefinisikan apa yang ia butuhkan;
// lapisan luar yang menyediakan.
package chat

import (
	"context"
	"errors"
	"time"
)

// Error domain. Lapisan transport memetakan ini ke status HTTP / kode WS,
// sehingga domain tidak perlu tahu soal 404 atau 403.
var (
	ErrNotFound     = errors.New("chat: percakapan tidak ditemukan")
	ErrNotMember    = errors.New("chat: pengguna bukan anggota percakapan ini")
	ErrEmptyBody    = errors.New("chat: isi pesan tidak boleh kosong")
	ErrBodyTooLong  = errors.New("chat: isi pesan melebihi batas")
	ErrInvalidInput = errors.New("chat: input tidak valid")
	ErrNotAllowed   = errors.New("chat: percakapan tidak diizinkan")
)

// Batas yang ditegakkan service.
const (
	MaxBodyLength   = 4000
	MaxTitleLength  = 100
	MaxGroupMembers = 256
	MaxAttachments  = 10
)

// ErrNotGroup dikembalikan saat operasi grup dipanggil pada chat pribadi.
var ErrNotGroup = errors.New("chat: bukan percakapan grup")

// ConversationType membedakan chat pribadi dan grup.
type ConversationType string

const (
	ConversationDirect ConversationType = "direct"
	ConversationGroup  ConversationType = "group"
)

// MessageType mengikuti enum MESSAGES.type di docs/erd.md.
type MessageType string

const (
	MessageText       MessageType = "text"
	MessageMedia      MessageType = "media"
	MessageVoiceNote  MessageType = "voice_note"
	MessageStoryReply MessageType = "story_reply"
	MessageCallEvent  MessageType = "call_event"
	MessageSystem     MessageType = "system"
)

// Conversation adalah satu baris di daftar chat.
//
// Bentuknya mengikuti apa yang benar-benar dirender klien: nama tampil,
// avatar, cuplikan pesan terakhir, dan jumlah belum dibaca. Untuk percakapan
// pribadi, Title dan AvatarMediaID sudah berisi milik lawan bicara — klien
// tidak perlu mencari sendiri siapa peserta lainnya.
type Conversation struct {
	ID            string
	Type          ConversationType
	Title         string
	AvatarMediaID string
	CounterpartID string // kosong untuk percakapan grup

	// CounterpartUsername adalah username lawan bicara pada percakapan direct —
	// dipakai layar panggilan masuk untuk menampilkan nama, dan fitur
	// laporkan/blokir dari daftar chat tanpa membuka percakapannya dulu.
	CounterpartUsername string

	// CounterpartLastReadID adalah pesan terakhir yang sudah dibaca lawan
	// bicara. Karena id memakai UUIDv7 yang terurut waktu, klien cukup
	// membandingkan id pesannya dengan nilai ini untuk memutuskan ✓✓ —
	// tanpa perlu tabel receipt per pesan.
	//
	// Hanya terisi untuk percakapan `direct`.
	CounterpartLastReadID string

	UnreadCount int

	LastMessagePreview string
	LastMessageType    MessageType
	LastMessageSender  string
	LastMessageAt      time.Time

	CreatedAt time.Time
}

// Message adalah satu pesan di dalam percakapan.
type Message struct {
	ID             string
	ConversationID string
	SenderID       string
	Type           MessageType
	Body           string
	ReplyToID      string
	CreatedAt      time.Time
	EditedAt       *time.Time

	// IsDeleted menandai pesan yang dihapus. Barisnya tetap dikirim ke klien
	// dengan Body kosong supaya urutan riwayat tidak berlubang.
	IsDeleted bool

	// AttachmentKeys adalah storage key lampiran yang DIKEMBALIKAN ke klien,
	// urut posisi. Kosong untuk pesan teks biasa.
	AttachmentKeys []string

	// MediaIDs adalah lampiran yang DIKIRIM saat membuat pesan (input). Tidak
	// ikut diserialkan ke klien.
	MediaIDs []string
}

// StarredMessage adalah pesan yang ditandai bintang oleh pemanggil, plus kapan
// ditandai. Bintang adalah penanda pribadi, tidak terlihat peserta lain.
type StarredMessage struct {
	Message
	StarredAt time.Time
}

// ConversationDetail adalah info satu percakapan untuk layar info grup.
type ConversationDetail struct {
	ID          string
	Type        ConversationType
	Title       string
	AvatarKey   string
	CreatedBy   string
	MyRole      string
	IsMuted     bool
	MemberCount int
	CreatedAt   time.Time
}

// Member adalah satu anggota percakapan.
type Member struct {
	UserID      string
	Username    string
	DisplayName string
	AvatarKey   string
	Role        string
	JoinedAt    time.Time
}

// Reaction adalah satu reaksi emoji pada sebuah pesan.
type Reaction struct {
	MessageID string
	UserID    string
	Emoji     string
}

// Repository adalah port penyimpanan. Diimplementasikan oleh
// internal/repository/supabase.
type Repository interface {
	ListConversations(ctx context.Context, userID string, limit int, before time.Time) ([]Conversation, error)
	ListMessages(ctx context.Context, conversationID, userID, before string, limit int) ([]Message, error)
	IsMember(ctx context.Context, conversationID, userID string) (bool, error)
	InsertMessage(ctx context.Context, msg Message) error
	MarkRead(ctx context.Context, conversationID, userID, messageID string) error
	CreateDirect(ctx context.Context, userID, otherID string) (string, error)
	CreateGroup(ctx context.Context, userID, title string, memberIDs []string) (string, error)
	DeleteMessage(ctx context.Context, messageID, userID string) (conversationID string, err error)
	EditMessage(ctx context.Context, messageID, userID, body string) (conversationID string, editedAt time.Time, err error)
	StarMessage(ctx context.Context, messageID, userID string) error
	UnstarMessage(ctx context.Context, messageID, userID string) error
	ListStarred(ctx context.Context, userID string, before time.Time, limit int) ([]StarredMessage, error)
	ClearConversation(ctx context.Context, conversationID, userID string) error
	DeleteConversation(ctx context.Context, conversationID, userID string) error

	GetConversation(ctx context.Context, conversationID, userID string) (ConversationDetail, error)
	ListMembers(ctx context.Context, conversationID, userID string) ([]Member, error)
	AddMembers(ctx context.Context, conversationID, userID string, memberIDs []string) (int, error)
	RemoveMember(ctx context.Context, conversationID, userID, memberID string) error
	Leave(ctx context.Context, conversationID, userID string) error
	UpdateGroup(ctx context.Context, conversationID, userID, title, avatarMediaID string) error
	SetMemberRole(ctx context.Context, conversationID, userID, memberID, role string) error
	Mute(ctx context.Context, conversationID, userID string, until *time.Time) error
	React(ctx context.Context, messageID, userID, emoji string) (conversationID string, err error)
	ListReactions(ctx context.Context, userID string, messageIDs []string) ([]Reaction, error)
}

// Publisher adalah port siaran realtime. Diimplementasikan oleh
// internal/transport/ws.
//
// Domain sengaja tidak mengenal WebSocket. Ia hanya menyatakan "kabarkan
// kejadian ini ke topik itu" — apakah transportnya socket, SSE, atau push
// notification adalah urusan lapisan luar.
type Publisher interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}
