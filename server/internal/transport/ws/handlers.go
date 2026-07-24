package ws

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ahmadadptr001/backend-syntra/internal/domain/chat"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/presence"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws/protocol"
)

// ChatService adalah bagian domain chat yang dibutuhkan transport ini.
//
// Interface didefinisikan di sisi pemakai (transport), bukan di sisi
// penyedia (domain). Dengan begitu transport hanya bergantung pada method
// yang benar-benar ia panggil, dan test-nya cukup memakai stub kecil.
type ChatService interface {
	SendMessage(ctx context.Context, in chat.SendMessageInput) (chat.Message, error)
	MarkRead(ctx context.Context, conversationID, userID, messageID string) error
}

// MembershipChecker dipakai untuk mengotorisasi langganan topik.
//
// Satu interface untuk dua jenis topik, karena keduanya menjawab pertanyaan
// yang sama: apakah orang ini berhak menyimak kanal tersebut.
type MembershipChecker interface {
	IsMember(ctx context.Context, conversationID, userID string) (bool, error)
	IsRoomParticipant(ctx context.Context, roomID, userID string) (bool, error)
}

// PresenceService adalah bagian domain presence yang dipakai transport ini.
type PresenceService interface {
	Online(ctx context.Context, userID string) error
	Offline(ctx context.Context, userID string) error
	Query(ctx context.Context, userIDs []string) (map[string]presence.Status, error)
	Visible(ctx context.Context, userID string) (bool, error)
}

const maxTopicsPerFrame = 50

var (
	errBadPayload    = errors.New("payload frame tidak sesuai")
	errTopicDenied   = errors.New("tidak berhak berlangganan topik ini")
	errTopicUnknown  = errors.New("format topik tidak dikenal")
	errTooManyTopics = errors.New("jumlah langganan melebihi batas")
	errNotSubscribed = errors.New("belum berlangganan topik ini")
)

// RegisterHandlers memasang seluruh handler frame ke router.
func RegisterHandlers(r *Router, chatSvc ChatService, members MembershipChecker, presenceSvc PresenceService) {
	r.SetErrorMapper(MapDomainError)

	r.Handle(protocol.TypePing, handlePing)
	r.Handle(protocol.TypeSubscribe, handleSubscribe(members))
	r.Handle(protocol.TypeUnsubscribe, handleUnsubscribe)
	r.Handle(protocol.TypeMessageSend, handleMessageSend(chatSvc))
	r.Handle(protocol.TypeMessageRead, handleMessageRead(chatSvc))
	r.Handle(protocol.TypeTypingStart, handleTyping(true))
	r.Handle(protocol.TypeTypingStop, handleTyping(false))
	r.Handle(protocol.TypePresenceQuery, handlePresenceQuery(presenceSvc))
	r.Handle(protocol.TypeRoomChat, handleRoomChat)
}

// MaxRoomChatLength membatasi panjang pesan di dalam room.
const MaxRoomChatLength = 500

type roomChatPayload struct {
	RoomID string `json:"room_id"`
	Body   string `json:"body"`
}

type roomChatEvent struct {
	RoomID    string    `json:"room_id"`
	SenderID  string    `json:"sender_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// handleRoomChat menyiarkan pesan teks di dalam voice room.
//
// TIDAK menyentuh database sama sekali, dan itu memang rancangannya: chat room
// bersifat efemeral. Ia hidup selama room hidup, lalu hilang bersamanya —
// tidak ada riwayat yang bisa dimuat ulang, dan memang tidak ada endpoint
// untuk itu. Klien yang bergabung di tengah room hanya akan melihat pesan
// sejak ia masuk.
//
// Karena tidak ada penyimpanan, tidak ada pula id pesan maupun ack. Pesan yang
// terkirim saat seseorang sedang terputus memang hilang untuknya — itu
// perilaku yang diinginkan, bukan cacat yang perlu ditambal.
//
// Otorisasinya memanfaatkan langganan yang sudah tervalidasi: kalau klien
// belum berlangganan topik room itu, berarti ia belum lolos pemeriksaan
// keanggotaan saat subscribe, jadi ia juga tidak boleh menyiarkan ke sana.
func handleRoomChat(ctx context.Context, c *Client, env protocol.Envelope) error {
	var payload roomChatPayload
	if err := env.DecodeData(&payload); err != nil {
		return errBadPayload
	}

	payload.Body = strings.TrimSpace(payload.Body)
	if payload.RoomID == "" || payload.Body == "" {
		return errBadPayload
	}
	if utf8.RuneCountInString(payload.Body) > MaxRoomChatLength {
		return errBadPayload
	}

	name := topic.Room(payload.RoomID)
	if !c.hub.IsSubscribed(c, name) {
		return errNotSubscribed
	}

	frame, err := protocol.EncodeEvent(protocol.TypeRoomMessage, roomChatEvent{
		RoomID:    payload.RoomID,
		SenderID:  c.UserID,
		Body:      payload.Body,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}

	return c.hub.Publish(ctx, name, frame)
}

// PresenceEvent adalah bentuk payload presence.update yang diterima klien.
type PresenceEvent struct {
	UserID   string     `json:"user_id"`
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

type presenceQueryPayload struct {
	UserIDs []string `json:"user_ids"`
}

// handlePresenceQuery menjawab status sekumpulan pengguna sekaligus.
//
// Klien menanyakan seluruh lawan bicara di daftar chat dalam satu frame,
// bukan satu per satu — satu perjalanan untuk seluruh layar.
func handlePresenceQuery(svc PresenceService) HandlerFunc {
	return func(ctx context.Context, c *Client, env protocol.Envelope) error {
		var payload presenceQueryPayload
		if err := env.DecodeData(&payload); err != nil {
			return errBadPayload
		}
		if len(payload.UserIDs) == 0 {
			return errBadPayload
		}

		statuses, err := svc.Query(ctx, payload.UserIDs)
		if err != nil {
			return err
		}

		events := make([]PresenceEvent, 0, len(statuses))
		for _, st := range statuses {
			events = append(events, toPresenceEvent(st))
		}

		ack, err := protocol.NewAck(env.Ref, events)
		if err != nil {
			return err
		}
		c.SendEnvelope(ack)
		return nil
	}
}

func toPresenceEvent(st presence.Status) PresenceEvent {
	event := PresenceEvent{UserID: st.UserID, Online: st.Online}
	if !st.LastSeen.IsZero() {
		last := st.LastSeen
		event.LastSeen = &last
	}
	return event
}

// BroadcastPresence menyiarkan perubahan status seorang pengguna ke sejumlah
// topik.
//
// Dipanggil saat klien selesai berlangganan (menjadi online) dan saat koneksi
// berakhir (menjadi offline). Disiarkan ke topik percakapan, bukan ke topik
// pengguna, karena yang peduli pada status seseorang adalah lawan bicaranya —
// bukan perangkat lain miliknya sendiri.
func BroadcastPresence(ctx context.Context, hub *Hub, userID string, online bool, topics []string) {
	if len(topics) == 0 {
		return
	}

	event := PresenceEvent{UserID: userID, Online: online}
	if !online {
		now := time.Now().UTC()
		event.LastSeen = &now
	}

	frame, err := protocol.EncodeEvent(protocol.TypePresenceUpdate, event)
	if err != nil {
		return
	}

	for _, name := range topics {
		if kind, _, ok := topic.Parse(name); ok && kind == topic.KindConversation {
			_ = hub.Publish(ctx, name, frame)
		}
	}
}

func handlePing(_ context.Context, c *Client, env protocol.Envelope) error {
	c.SendEnvelope(protocol.Envelope{Type: protocol.TypePong, Ref: env.Ref})
	return nil
}

type topicsPayload struct {
	Topics []string `json:"topics"`
}

// handleSubscribe melanggankan klien ke topik SETELAH memeriksa hak akses.
//
// Ini titik paling rawan di seluruh lapisan socket. Tanpa pemeriksaan, siapa
// pun yang punya token valid bisa mengirim {"type":"subscribe","data":
// {"topics":["conversation:<id-orang-lain>"]}} dan ikut menyimak percakapan
// yang bukan miliknya. Otorisasi wajib per topik, bukan sekadar per koneksi.
func handleSubscribe(members MembershipChecker) HandlerFunc {
	return func(ctx context.Context, c *Client, env protocol.Envelope) error {
		var payload topicsPayload
		if err := env.DecodeData(&payload); err != nil {
			return errBadPayload
		}
		if len(payload.Topics) == 0 || len(payload.Topics) > maxTopicsPerFrame {
			return errBadPayload
		}
		if c.hub.TopicCount(c)+len(payload.Topics) > c.opts.MaxTopics {
			return errTooManyTopics
		}

		granted := make([]string, 0, len(payload.Topics))
		for _, name := range payload.Topics {
			if err := authorizeTopic(ctx, c, name, members); err != nil {
				return err
			}
			granted = append(granted, name)
		}

		c.hub.Subscribe(c, granted...)

		// Begitu klien menyimak sebuah percakapan, lawan bicaranya perlu tahu
		// ia sedang online. Disiarkan di sini, bukan saat koneksi terbentuk,
		// karena saat itu belum ada topik yang dilanggan sehingga belum ada
		// siapa pun yang bisa dituju. Dilewati bila pengguna menyembunyikan
		// presence-nya.
		if c.TrackPresence {
			BroadcastPresence(ctx, c.hub, c.UserID, true, granted)
		}

		ack, err := protocol.NewAck(env.Ref, topicsPayload{Topics: granted})
		if err != nil {
			return err
		}
		c.SendEnvelope(ack)
		return nil
	}
}

func handleUnsubscribe(_ context.Context, c *Client, env protocol.Envelope) error {
	var payload topicsPayload
	if err := env.DecodeData(&payload); err != nil {
		return errBadPayload
	}

	c.hub.Unsubscribe(c, payload.Topics...)

	ack, err := protocol.NewAck(env.Ref, topicsPayload{Topics: payload.Topics})
	if err != nil {
		return err
	}
	c.SendEnvelope(ack)
	return nil
}

// authorizeTopic memutuskan apakah klien boleh mendengarkan sebuah topik.
func authorizeTopic(ctx context.Context, c *Client, name string, members MembershipChecker) error {
	kind, entityID, ok := topic.Parse(name)
	if !ok {
		return errTopicUnknown
	}

	switch kind {
	case topic.KindUser:
		// Kanal pribadi hanya boleh didengar pemiliknya.
		if entityID != c.UserID {
			return errTopicDenied
		}
		return nil

	case topic.KindConversation:
		isMember, err := members.IsMember(ctx, entityID, c.UserID)
		if err != nil {
			return err
		}
		if !isMember {
			return errTopicDenied
		}
		return nil

	case topic.KindRoom:
		// Otorisasinya bersandar pada keanggotaan yang tercatat saat join_room,
		// dan join_room sendiri sudah memeriksa visibility, blokir, serta
		// kapasitas. Jadi peserta aktif memang berhak menyimak kanal ini.
		//
		// Yang lewat kanal ini hanya event (siapa masuk, siapa naik jadi
		// speaker). Audionya lewat SFU, bukan lewat sini.
		isParticipant, err := members.IsRoomParticipant(ctx, entityID, c.UserID)
		if err != nil {
			return err
		}
		if !isParticipant {
			return errTopicDenied
		}
		return nil

	case topic.KindReel:
		// Kanal satu reel: counter like/komentar realtime. Boleh disimak siapa
		// pun yang terautentikasi — yang lewat hanya angka & teks komentar
		// publik, bukan data sensitif. Klien melanggan saat reel tampil.
		return nil

	case topic.KindRoomsFeed, topic.KindReelsFeed:
		// Feed global: setiap pengguna terautentikasi boleh menyimak agar tahu
		// ada room/reel baru saat tab-nya terbuka. Aman karena yang lewat hanya
		// penanda ringkas, dan hanya konten publik yang diumumkan ke sini —
		// room followers/invite_only dan reel followers/private tidak disiarkan.
		return nil

	default:
		return errTopicUnknown
	}
}

type sendMessagePayload struct {
	ConversationID string `json:"conversation_id"`
	Type           string `json:"type,omitempty"`
	Body           string `json:"body,omitempty"`
	ReplyToID      string `json:"reply_to_id,omitempty"`
}

// handleMessageSend meneruskan pesan ke service domain.
//
// Perhatikan betapa tipisnya handler ini: decode, panggil service, balas ack.
// Tidak ada validasi bisnis dan tidak ada query. Handler REST untuk kirim
// pesan akan terlihat sama tipisnya dan memanggil service yang sama persis.
func handleMessageSend(svc ChatService) HandlerFunc {
	return func(ctx context.Context, c *Client, env protocol.Envelope) error {
		var payload sendMessagePayload
		if err := env.DecodeData(&payload); err != nil {
			return errBadPayload
		}

		msg, err := svc.SendMessage(ctx, chat.SendMessageInput{
			ConversationID: payload.ConversationID,
			SenderID:       c.UserID,
			Type:           chat.MessageType(payload.Type),
			Body:           payload.Body,
			ReplyToID:      payload.ReplyToID,
		})
		if err != nil {
			return err
		}

		// Ack membawa id dan waktu final dari server, supaya klien bisa
		// mengganti pesan optimistik di layar dengan yang otoritatif.
		ack, err := protocol.NewAck(env.Ref, map[string]any{
			"id":              msg.ID,
			"conversation_id": msg.ConversationID,
			"created_at":      msg.CreatedAt,
		})
		if err != nil {
			return err
		}
		c.SendEnvelope(ack)
		return nil
	}
}

type readPayload struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
}

func handleMessageRead(svc ChatService) HandlerFunc {
	return func(ctx context.Context, c *Client, env protocol.Envelope) error {
		var payload readPayload
		if err := env.DecodeData(&payload); err != nil {
			return errBadPayload
		}

		if err := svc.MarkRead(ctx, payload.ConversationID, c.UserID, payload.MessageID); err != nil {
			return err
		}

		ack, err := protocol.NewAck(env.Ref, nil)
		if err != nil {
			return err
		}
		c.SendEnvelope(ack)
		return nil
	}
}

type typingPayload struct {
	ConversationID string `json:"conversation_id"`
}

type typingEvent struct {
	ConversationID string `json:"conversation_id"`
	UserID         string `json:"user_id"`
	Typing         bool   `json:"typing"`
}

// handleTyping menyiarkan indikator mengetik.
//
// Tidak menyentuh database sama sekali — ini state efemeral. Otorisasinya
// memanfaatkan langganan yang sudah tervalidasi: kalau klien belum
// berlangganan topik percakapan itu, berarti ia belum lolos pemeriksaan
// keanggotaan, jadi ia juga tidak boleh menyiarkan ke sana.
func handleTyping(typing bool) HandlerFunc {
	return func(ctx context.Context, c *Client, env protocol.Envelope) error {
		var payload typingPayload
		if err := env.DecodeData(&payload); err != nil {
			return errBadPayload
		}
		if payload.ConversationID == "" {
			return errBadPayload
		}

		name := topic.Conversation(payload.ConversationID)
		if !c.hub.IsSubscribed(c, name) {
			return errNotSubscribed
		}

		frame, err := protocol.EncodeEvent(chat.EventTyping, typingEvent{
			ConversationID: payload.ConversationID,
			UserID:         c.UserID,
			Typing:         typing,
		})
		if err != nil {
			return err
		}

		return c.hub.Publish(ctx, name, frame)
	}
}

// MapDomainError menerjemahkan error menjadi kode protokol.
//
// Pesan yang dikembalikan ditujukan untuk pengguna akhir; error asli tetap
// tercatat di log server oleh Router.
func MapDomainError(err error) (string, string) {
	switch {
	case errors.Is(err, errBadPayload),
		errors.Is(err, errTopicUnknown),
		errors.Is(err, chat.ErrInvalidInput),
		errors.Is(err, chat.ErrEmptyBody),
		errors.Is(err, chat.ErrBodyTooLong):
		return protocol.CodeBadRequest, err.Error()

	case errors.Is(err, errTopicDenied),
		errors.Is(err, errNotSubscribed),
		errors.Is(err, chat.ErrNotMember):
		return protocol.CodeForbidden, "kamu tidak punya akses ke sumber daya ini"

	case errors.Is(err, chat.ErrNotFound):
		return protocol.CodeNotFound, "sumber daya tidak ditemukan"

	case errors.Is(err, errTooManyTopics):
		return protocol.CodeRateLimited, err.Error()

	default:
		return protocol.CodeInternal, "terjadi kesalahan internal"
	}
}
