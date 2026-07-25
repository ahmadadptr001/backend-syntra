package chat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/topic"
)

const (
	defaultPageSize    = 30
	defaultMessagePage = 50
	maxPageSize        = 100
)

// Service memuat alur bisnis chat.
//
// Perhatikan bahwa service ini tidak punya varian "untuk REST" dan "untuk
// WebSocket". Keduanya memanggil method yang sama persis. Inilah alasan
// socket dan API digabung dalam satu codebase: kalau logikanya terpisah,
// aturan seperti pengecekan keanggotaan cepat atau lambat akan berbeda
// antara dua jalur, dan yang lebih longgar menjadi celah keamanan.
type Service struct {
	repo     Repository
	pub      Publisher
	log      *slog.Logger
	mediaURL func(storageKey string) string
}

// NewService merangkai service dengan port yang dibutuhkannya. mediaURL
// menyusun URL publik dari storage key lampiran, supaya siaran message.new
// membawa URL siap tampil (kalau nil, lampiran tidak ikut disiarkan).
func NewService(repo Repository, pub Publisher, log *slog.Logger, mediaURL func(string) string) *Service {
	return &Service{repo: repo, pub: pub, log: log, mediaURL: mediaURL}
}

// ListConversations mengembalikan daftar chat milik pengguna, terbaru dulu.
// before dipakai sebagai cursor; kirim zero value untuk halaman pertama.
func (s *Service) ListConversations(ctx context.Context, userID string, limit int, before time.Time) ([]Conversation, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}

	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	if before.IsZero() {
		before = time.Now().Add(time.Minute)
	}

	return s.repo.ListConversations(ctx, userID, limit, before)
}

// ListMessages mengembalikan riwayat sebuah percakapan, terbaru dulu.
//
// before adalah id pesan sebagai cursor — kirim string kosong untuk halaman
// pertama. Id memakai UUIDv7 yang terurut waktu, jadi cursor berbasis id lebih
// tepat daripada berbasis timestamp: dua pesan bisa punya waktu yang identik,
// dan cursor waktu akan melewatkan salah satunya.
func (s *Service) ListMessages(ctx context.Context, conversationID, userID, before string, limit int) ([]Message, error) {
	if conversationID == "" || userID == "" {
		return nil, ErrInvalidInput
	}

	switch {
	case limit <= 0:
		limit = defaultMessagePage
	case limit > maxPageSize:
		limit = maxPageSize
	}

	return s.repo.ListMessages(ctx, conversationID, userID, before, limit)
}

// StartDirect membuka percakapan pribadi dengan seseorang.
//
// Operasi ini idempoten: memanggilnya lagi untuk orang yang sama mengembalikan
// percakapan yang sudah ada. Klien boleh memanggilnya setiap kali pengguna
// menekan "kirim pesan" tanpa perlu memeriksa dulu.
func (s *Service) StartDirect(ctx context.Context, userID, otherID string) (string, error) {
	if userID == "" || otherID == "" {
		return "", ErrInvalidInput
	}
	if userID == otherID {
		return "", ErrInvalidInput
	}
	return s.repo.CreateDirect(ctx, userID, otherID)
}

// CreateGroup membuat percakapan grup dengan pemanggil sebagai pemilik.
func (s *Service) CreateGroup(ctx context.Context, userID, title string, memberIDs []string) (string, error) {
	title = strings.TrimSpace(title)

	switch {
	case userID == "" || title == "":
		return "", ErrInvalidInput
	case utf8.RuneCountInString(title) > MaxTitleLength:
		return "", ErrInvalidInput
	case len(memberIDs) > MaxGroupMembers:
		return "", ErrInvalidInput
	}

	return s.repo.CreateGroup(ctx, userID, title, memberIDs)
}

// SendMessageInput adalah permintaan kirim pesan.
type SendMessageInput struct {
	ConversationID string
	SenderID       string
	Type           MessageType
	Body           string
	ReplyToID      string

	// MediaIDs adalah lampiran — foto, voice note, dsb. Media harus sudah
	// dikonfirmasi lebih dulu dan milik pengirim.
	MediaIDs []string
}

// SendMessage memvalidasi, menyimpan, lalu menyiarkan pesan.
func (s *Service) SendMessage(ctx context.Context, in SendMessageInput) (Message, error) {
	if in.ConversationID == "" || in.SenderID == "" {
		return Message{}, ErrInvalidInput
	}

	if in.Type == "" {
		in.Type = MessageText
	}

	in.Body = strings.TrimSpace(in.Body)

	// Pesan teks tanpa lampiran wajib berisi. Pesan dengan lampiran boleh
	// tanpa teks — kiriman foto polos, misalnya.
	if in.Type == MessageText && len(in.MediaIDs) == 0 {
		if in.Body == "" {
			return Message{}, ErrEmptyBody
		}
	}
	if utf8.RuneCountInString(in.Body) > MaxBodyLength {
		return Message{}, ErrBodyTooLong
	}
	if len(in.MediaIDs) > MaxAttachments {
		return Message{}, ErrInvalidInput
	}

	// Otorisasi ada di service, bukan di handler. Handler bisa bertambah
	// (REST, WS, job internal); aturannya harus tetap satu.
	member, err := s.repo.IsMember(ctx, in.ConversationID, in.SenderID)
	if err != nil {
		return Message{}, fmt.Errorf("chat: gagal memeriksa keanggotaan: %w", err)
	}
	if !member {
		return Message{}, ErrNotMember
	}

	if in.Type == MessageText && len(in.MediaIDs) > 0 {
		in.Type = MessageMedia
	}

	msg := Message{
		ID:             id.New(),
		ConversationID: in.ConversationID,
		SenderID:       in.SenderID,
		Type:           in.Type,
		Body:           in.Body,
		ReplyToID:      in.ReplyToID,
		CreatedAt:      time.Now().UTC(),
		MediaIDs:       in.MediaIDs,
	}

	if err := s.repo.InsertMessage(ctx, msg); err != nil {
		return Message{}, fmt.Errorf("chat: gagal menyimpan pesan: %w", err)
	}

	// Resolusikan id media menjadi storage key SEKARANG, supaya baik siaran
	// message.new maupun respons REST membawa URL lampiran — kalau tidak, foto/
	// pesan suara muncul sebagai bubble kosong sampai chat dimuat ulang.
	if len(msg.MediaIDs) > 0 {
		if keys, err := s.repo.AttachmentKeys(ctx, in.SenderID, msg.MediaIDs); err != nil {
			s.log.Warn("chat: gagal resolusi lampiran untuk siaran",
				"error", err, "message_id", msg.ID)
		} else {
			msg.AttachmentKeys = keys
		}
	}

	// Siaran gagal tidak membatalkan pesan yang sudah tersimpan. Pesannya nyata
	// dan sudah durabel; klien lain akan mendapatkannya saat sinkronisasi ulang.
	// Mengembalikan error di sini justru membuat pengirim mengira pesannya gagal
	// dan mengirim ulang — duplikat, bukan perbaikan.
	if err := s.pub.Publish(ctx, topic.Conversation(msg.ConversationID), EventMessageNew, s.newMessageEvent(msg)); err != nil {
		s.log.Warn("chat: pesan tersimpan tapi gagal disiarkan",
			"error", err,
			"message_id", msg.ID,
			"conversation_id", msg.ConversationID,
		)
	}

	return msg, nil
}

// GetConversation mengembalikan info satu percakapan (untuk layar info grup).
func (s *Service) GetConversation(ctx context.Context, conversationID, userID string) (ConversationDetail, error) {
	if conversationID == "" || userID == "" {
		return ConversationDetail{}, ErrInvalidInput
	}
	return s.repo.GetConversation(ctx, conversationID, userID)
}

// Members mengembalikan daftar anggota percakapan.
func (s *Service) Members(ctx context.Context, conversationID, userID string) ([]Member, error) {
	if conversationID == "" || userID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.ListMembers(ctx, conversationID, userID)
}

// AddMembers menambah anggota ke grup. Hanya admin/owner.
func (s *Service) AddMembers(ctx context.Context, conversationID, userID string, memberIDs []string) (int, error) {
	if conversationID == "" || userID == "" || len(memberIDs) == 0 {
		return 0, ErrInvalidInput
	}
	if len(memberIDs) > MaxGroupMembers {
		return 0, ErrInvalidInput
	}
	added, err := s.repo.AddMembers(ctx, conversationID, userID, memberIDs)
	if err != nil {
		return 0, err
	}
	s.broadcastConversation(ctx, conversationID)
	return added, nil
}

// RemoveMember mengeluarkan anggota. Hanya admin/owner; owner tak bisa dikeluarkan.
func (s *Service) RemoveMember(ctx context.Context, conversationID, userID, memberID string) error {
	if conversationID == "" || userID == "" || memberID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.RemoveMember(ctx, conversationID, userID, memberID); err != nil {
		return err
	}
	s.broadcastConversation(ctx, conversationID)
	return nil
}

// Leave mengeluarkan pemanggil dari grup. Kalau owner keluar, kepemilikan
// diwariskan ke anggota terlama.
func (s *Service) Leave(ctx context.Context, conversationID, userID string) error {
	if conversationID == "" || userID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.Leave(ctx, conversationID, userID); err != nil {
		return err
	}
	s.broadcastConversation(ctx, conversationID)
	return nil
}

// UpdateGroup mengubah judul dan/atau avatar grup. Hanya admin/owner.
func (s *Service) UpdateGroup(ctx context.Context, conversationID, userID, title, avatarMediaID string) error {
	title = strings.TrimSpace(title)
	if conversationID == "" || userID == "" {
		return ErrInvalidInput
	}
	if title != "" && utf8.RuneCountInString(title) > MaxTitleLength {
		return ErrInvalidInput
	}
	if err := s.repo.UpdateGroup(ctx, conversationID, userID, title, avatarMediaID); err != nil {
		return err
	}
	s.broadcastConversation(ctx, conversationID)
	return nil
}

// SetMemberRole menjadikan anggota admin atau menurunkannya. Hanya owner.
func (s *Service) SetMemberRole(ctx context.Context, conversationID, userID, memberID, role string) error {
	if conversationID == "" || userID == "" || memberID == "" {
		return ErrInvalidInput
	}
	if role != "admin" && role != "member" {
		return ErrInvalidInput
	}
	if err := s.repo.SetMemberRole(ctx, conversationID, userID, memberID, role); err != nil {
		return err
	}
	s.broadcastConversation(ctx, conversationID)
	return nil
}

// Mute membisukan percakapan sampai waktu tertentu. until nil = bunyikan lagi.
func (s *Service) Mute(ctx context.Context, conversationID, userID string, until *time.Time) error {
	if conversationID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.Mute(ctx, conversationID, userID, until)
}

// React menambah/mengubah reaksi emoji pada pesan. emoji kosong menghapusnya.
func (s *Service) React(ctx context.Context, messageID, userID, emoji string) error {
	if messageID == "" || userID == "" {
		return ErrInvalidInput
	}
	conversationID, err := s.repo.React(ctx, messageID, userID, emoji)
	if err != nil {
		return err
	}

	// Siarkan supaya reaksi (tambah/ganti/hapus — emoji kosong = hapus) muncul
	// realtime di layar peserta lain tanpa perlu memuat ulang reaksi pesan.
	if err := s.pub.Publish(ctx, topic.Conversation(conversationID), EventMessageReaction, ReactionEvent{
		MessageID: messageID, ConversationID: conversationID, UserID: userID, Emoji: emoji,
	}); err != nil {
		s.log.Warn("chat: reaksi tersimpan tapi gagal disiarkan",
			"error", err, "message_id", messageID, "conversation_id", conversationID)
	}
	return nil
}

// Reactions mengembalikan reaksi untuk sekumpulan pesan sekaligus.
func (s *Service) Reactions(ctx context.Context, userID string, messageIDs []string) ([]Reaction, error) {
	if userID == "" || len(messageIDs) == 0 {
		return nil, ErrInvalidInput
	}
	return s.repo.ListReactions(ctx, userID, messageIDs)
}

// broadcastConversation memberi tahu peserta bahwa keanggotaan/info berubah,
// supaya layar info grup menyegarkan diri tanpa polling.
func (s *Service) broadcastConversation(ctx context.Context, conversationID string) {
	_ = s.pub.Publish(ctx, topic.Conversation(conversationID), EventConversationUpdated, map[string]any{
		"conversation_id": conversationID,
	})
}

// DeleteMessage menghapus pesan milik pemanggil.
//
// Soft delete: barisnya tetap dikirim ke klien dengan is_deleted=true supaya
// urutan riwayat tidak berlubang, tetapi isinya dikosongkan.
func (s *Service) DeleteMessage(ctx context.Context, messageID, userID string) error {
	if messageID == "" || userID == "" {
		return ErrInvalidInput
	}
	conversationID, err := s.repo.DeleteMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}

	// Siarkan supaya perangkat lawan bicara menandai "pesan ini dihapus"
	// seketika, alih-alih baru tahu saat chat dibuka ulang.
	if err := s.pub.Publish(ctx, topic.Conversation(conversationID), EventMessageDeleted, MessageDeletedEvent{
		MessageID: messageID, ConversationID: conversationID,
	}); err != nil {
		s.log.Warn("chat: pesan dihapus tapi gagal disiarkan",
			"error", err, "message_id", messageID, "conversation_id", conversationID)
	}
	return nil
}

// EditMessage mengubah isi pesan teks milik pemanggil, lalu menyiarkan
// message.updated supaya perangkat lain mengganti isinya di tempat.
func (s *Service) EditMessage(ctx context.Context, messageID, userID, body string) error {
	if messageID == "" || userID == "" {
		return ErrInvalidInput
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return ErrEmptyBody
	}
	if utf8.RuneCountInString(body) > MaxBodyLength {
		return ErrBodyTooLong
	}

	conversationID, editedAt, err := s.repo.EditMessage(ctx, messageID, userID, body)
	if err != nil {
		return err
	}

	if err := s.pub.Publish(ctx, topic.Conversation(conversationID), EventMessageUpdated, MessageUpdatedEvent{
		ID: messageID, ConversationID: conversationID, Body: body, EditedAt: editedAt,
	}); err != nil {
		s.log.Warn("chat: pesan diedit tapi gagal disiarkan",
			"error", err, "message_id", messageID, "conversation_id", conversationID)
	}
	return nil
}

// StarMessage menandai pesan sebagai bintang bagi pemanggil. Idempoten.
func (s *Service) StarMessage(ctx context.Context, messageID, userID string) error {
	if messageID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.StarMessage(ctx, messageID, userID)
}

// UnstarMessage melepas bintang. Idempoten.
func (s *Service) UnstarMessage(ctx context.Context, messageID, userID string) error {
	if messageID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.UnstarMessage(ctx, messageID, userID)
}

// ListStarred mengembalikan pesan berbintang pemanggil lintas percakapan,
// terbaru dulu.
func (s *Service) ListStarred(ctx context.Context, userID string, before time.Time, limit int) ([]StarredMessage, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}
	if before.IsZero() {
		before = time.Now().Add(time.Minute)
	}
	return s.repo.ListStarred(ctx, userID, before, limit)
}

// ClearConversation mengosongkan riwayat percakapan HANYA untuk pemanggil.
//
// Menghapus pesan orang lain dari layar mereka bukan wewenang siapa pun di
// percakapan, jadi yang dicatat adalah batas baca: pesan lama disembunyikan
// dari pemanggil, sementara peserta lain tetap melihat riwayatnya utuh.
func (s *Service) ClearConversation(ctx context.Context, conversationID, userID string) error {
	if conversationID == "" || userID == "" {
		return ErrInvalidInput
	}
	return s.repo.ClearConversation(ctx, conversationID, userID)
}

// DeleteConversation menghapus seluruh obrolan dari sisi pemanggil.
//
// Untuk grup: keluar (dengan alih kepemilikan bila owner) lalu sembunyikan dari
// daftar. Untuk direct: sembunyikan dari daftar dan bersihkan tampilannya.
// Peserta lain tidak terpengaruh. Untuk grup, siaran conversation.updated
// memberi tahu anggota tersisa bahwa pemanggil telah keluar.
func (s *Service) DeleteConversation(ctx context.Context, conversationID, userID string) error {
	if conversationID == "" || userID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.DeleteConversation(ctx, conversationID, userID); err != nil {
		return err
	}
	s.broadcastConversation(ctx, conversationID)
	return nil
}

// MarkRead menandai percakapan sudah dibaca sampai messageID tertentu.
func (s *Service) MarkRead(ctx context.Context, conversationID, userID, messageID string) error {
	if conversationID == "" || userID == "" || messageID == "" {
		return ErrInvalidInput
	}

	member, err := s.repo.IsMember(ctx, conversationID, userID)
	if err != nil {
		return fmt.Errorf("chat: gagal memeriksa keanggotaan: %w", err)
	}
	if !member {
		return ErrNotMember
	}

	if err := s.repo.MarkRead(ctx, conversationID, userID, messageID); err != nil {
		return fmt.Errorf("chat: gagal menandai dibaca: %w", err)
	}

	// Disiarkan ke topik PERCAKAPAN supaya LAWAN BICARA tahu pesannya sudah
	// dibaca (centang biru), sekaligus menyelaraskan badge antar perangkat si
	// pembaca. UserID disertakan agar klien membedakan: kalau UserID == dirinya,
	// itu sinkron badge; kalau UserID == lawan, itulah pemicu centang biru.
	event := ReadEvent{
		ConversationID: conversationID,
		UserID:         userID,
		MessageID:      messageID,
		ReadAt:         time.Now().UTC(),
	}
	if err := s.pub.Publish(ctx, topic.Conversation(conversationID), EventMessageRead, event); err != nil {
		s.log.Warn("chat: gagal menyiarkan status dibaca", "error", err, "user_id", userID)
	}

	return nil
}

// MarkDelivered menandai pesan sudah SAMPAI di perangkat userID (✓✓ abu), lalu
// menyiarkannya supaya pengirim menaikkan centangnya secara realtime. Disimpan
// (bukan sekadar disiarkan) agar status tetap akurat setelah aplikasi ditutup.
func (s *Service) MarkDelivered(ctx context.Context, conversationID, userID, messageID string) error {
	if conversationID == "" || userID == "" || messageID == "" {
		return ErrInvalidInput
	}

	member, err := s.repo.IsMember(ctx, conversationID, userID)
	if err != nil {
		return fmt.Errorf("chat: gagal memeriksa keanggotaan: %w", err)
	}
	if !member {
		return ErrNotMember
	}

	if err := s.repo.MarkDelivered(ctx, conversationID, userID, messageID); err != nil {
		return fmt.Errorf("chat: gagal menandai sampai: %w", err)
	}

	// UserID = si penerima. Pengirim memakainya untuk menaikkan ✓ menjadi ✓✓
	// pada pesan miliknya; echo ke perangkat penerima sendiri diabaikan klien.
	event := DeliveredEvent{
		ConversationID: conversationID,
		UserID:         userID,
		MessageID:      messageID,
	}
	if err := s.pub.Publish(ctx, topic.Conversation(conversationID), EventMessageDelivered, event); err != nil {
		s.log.Warn("chat: gagal menyiarkan status sampai", "error", err, "user_id", userID)
	}

	return nil
}
