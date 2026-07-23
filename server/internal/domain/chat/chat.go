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
)

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
