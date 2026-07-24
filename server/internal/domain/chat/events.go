package chat

import "time"

// Nama event yang disiarkan domain chat. Konstanta ini adalah kontrak dengan
// klien Kotlin — mengubah nilainya berarti merusak aplikasi yang sudah rilis.
const (
	EventMessageNew          = "message.new"
	EventMessageUpdated      = "message.updated"
	EventMessageDeleted      = "message.deleted"
	EventMessageReaction     = "message.reaction"
	EventMessageRead         = "message.read"
	EventTyping              = "typing"
	EventConversationUpdated = "conversation.updated"
)

// MessageDeletedEvent disiarkan saat sebuah pesan dihapus, supaya perangkat lain
// menandainya "pesan ini dihapus" seketika alih-alih menunggu chat dibuka ulang.
type MessageDeletedEvent struct {
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
}

// ReactionEvent disiarkan saat reaksi ditambah, diganti, atau dihapus (emoji
// kosong = dihapus), supaya reaksi muncul realtime di layar peserta lain.
type ReactionEvent struct {
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	UserID         string `json:"user_id"`
	Emoji          string `json:"emoji,omitempty"`
}

// MessageUpdatedEvent disiarkan saat sebuah pesan diedit, supaya perangkat lain
// mengganti isinya di tempat alih-alih menambah baris baru.
type MessageUpdatedEvent struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Body           string    `json:"body"`
	EditedAt       time.Time `json:"edited_at"`
}

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
	// Attachments adalah URL lampiran siap tampil, supaya foto/voice note muncul
	// realtime tanpa perlu memuat ulang percakapan.
	Attachments []string `json:"attachments,omitempty"`
}

// ReadEvent memberi tahu perangkat lain milik pengguna yang sama bahwa
// percakapan sudah dibaca, supaya badge unread ikut turun di semua perangkat.
type ReadEvent struct {
	ConversationID string    `json:"conversation_id"`
	UserID         string    `json:"user_id"`
	MessageID      string    `json:"message_id"`
	ReadAt         time.Time `json:"read_at"`
}

func (s *Service) newMessageEvent(m Message) MessageEvent {
	// Resolve attachment storage keys to public URLs so clients render media
	// immediately from the broadcast, not only after a history reload.
	var attachments []string
	if s.mediaURL != nil {
		for _, key := range m.AttachmentKeys {
			if url := s.mediaURL(key); url != "" {
				attachments = append(attachments, url)
			}
		}
	}
	return MessageEvent{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		SenderID:       m.SenderID,
		Type:           m.Type,
		Body:           m.Body,
		ReplyToID:      m.ReplyToID,
		CreatedAt:      m.CreatedAt,
		Attachments:    attachments,
	}
}
