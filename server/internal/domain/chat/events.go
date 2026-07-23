package chat

import "time"

// Nama event yang disiarkan domain chat. Konstanta ini adalah kontrak dengan
// klien Kotlin — mengubah nilainya berarti merusak aplikasi yang sudah rilis.
const (
	EventMessageNew  = "message.new"
	EventMessageRead = "message.read"
	EventTyping      = "typing"
)

// MessageEvent adalah bentuk payload yang dikirim ke klien.
//
// Tipe ini punya JSON tag padahal berada di domain, dan itu disengaja: bentuk
// event yang dipancarkan adalah bagian dari kontrak publik domain, sama seperti
// signature fungsinya. Yang tidak boleh bocor ke sini adalah detail transport
// (header, status code, frame WebSocket) — dan itu tetap tidak ada.
type MessageEvent struct {
	ID             string      `json:"id"`
	ConversationID string      `json:"conversation_id"`
	SenderID       string      `json:"sender_id"`
	Type           MessageType `json:"type"`
	Body           string      `json:"body,omitempty"`
	ReplyToID      string      `json:"reply_to_id,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
}

// ReadEvent memberi tahu perangkat lain milik pengguna yang sama bahwa
// percakapan sudah dibaca, supaya badge unread ikut turun di semua perangkat.
type ReadEvent struct {
	ConversationID string    `json:"conversation_id"`
	UserID         string    `json:"user_id"`
	MessageID      string    `json:"message_id"`
	ReadAt         time.Time `json:"read_at"`
}

func newMessageEvent(m Message) MessageEvent {
	return MessageEvent{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		SenderID:       m.SenderID,
		Type:           m.Type,
		Body:           m.Body,
		ReplyToID:      m.ReplyToID,
		CreatedAt:      m.CreatedAt,
	}
}
