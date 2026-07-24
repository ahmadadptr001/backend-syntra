// Package call mengurus panggilan suara dan video di dalam percakapan.
//
// Sama seperti voice room, backend TIDAK mengalirkan audio maupun video — itu
// tugas SFU (LiveKit). Backend mencatat sesi, mengotorisasi peserta, dan
// menerbitkan token. Bedanya: panggilan terikat pada sebuah percakapan dan
// punya siklus dering → jawab/tolak → selesai, plus siaran realtime supaya
// perangkat lawan bicara benar-benar berdering.
package call

import (
	"context"
	"errors"
	"time"

	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
)

var (
	ErrInvalidInput = errors.New("call: input tidak valid")
	ErrNotFound     = errors.New("call: panggilan tidak ditemukan")
	ErrNotAllowed   = errors.New("call: tidak diizinkan")
	ErrNoSFU        = errors.New("call: media server belum dikonfigurasi")
	ErrWebhookAuth  = errors.New("call: tanda tangan webhook tidak sah")
)

// Kind membedakan panggilan suara dan video.
type Kind string

const (
	KindAudio Kind = "audio"
	KindVideo Kind = "video"
)

// Valid memeriksa jenis panggilan.
func (k Kind) Valid() bool { return k == KindAudio || k == KindVideo }

// Nama event yang disiarkan ke topik percakapan.
const (
	EventIncoming = "call.incoming" // ke peserta lain saat panggilan dimulai
	EventAnswered = "call.answered"
	EventEnded    = "call.ended"
)

// Nama event webhook LiveKit yang diproses backend. Selain ini diabaikan.
const (
	SFUParticipantLeftEvent = "participant_left"
	SFURoomFinishedEvent    = "room_finished"
)

// SFUResult adalah hasil memproses satu event webhook: panggilan mana yang
// tersentuh, di percakapan mana, dan apakah panggilannya berakhir karenanya.
type SFUResult struct {
	CallID         string
	ConversationID string
	Ended          bool
}

// Session adalah hasil memulai/menjawab panggilan — bekal menyambung ke SFU.
type Session struct {
	CallID    string
	SFURoomID string
	SFUToken  string
	SFUURL    string
	IsNew     bool
}

// Active adalah panggilan yang sedang berlangsung pada sebuah percakapan.
type Active struct {
	ID          string
	Kind        Kind
	Status      string
	InitiatorID string
	SFURoomID   string
	StartedAt   time.Time
}

// Repository adalah port penyimpanan.
type Repository interface {
	Start(ctx context.Context, callID, conversationID, kind, sfuRoom string) (id, sfu string, isNew bool, err error)
	Answer(ctx context.Context, callID string) (sfuRoom string, err error)
	Decline(ctx context.Context, callID string) error
	Leave(ctx context.Context, callID string) error
	GetActive(ctx context.Context, conversationID string) (*Active, error)

	// Dipicu webhook LiveKit, tanpa JWT pengguna. SFUParticipantLeft menandai
	// satu peserta keluar; SFURoomFinished menutup seluruh panggilan. Keduanya
	// mengembalikan nil kalau tidak ada panggilan aktif untuk room itu.
	SFUParticipantLeft(ctx context.Context, sfuRoom, identity string) (*SFUResult, error)
	SFURoomFinished(ctx context.Context, sfuRoom string) (*SFUResult, error)
}

// TokenIssuer menerbitkan kredensial SFU — sama dengan yang dipakai voice room.
type TokenIssuer interface {
	Issue(roomID, userID, identity string, canPublish bool) (token, url string, err error)
	Configured() bool
}

// WebhookVerifier memverifikasi lalu mengurai kiriman webhook dari SFU.
//
// Dipisah dari TokenIssuer meski di produksi keduanya diisi objek yang sama
// (livekit.Issuer): menerbitkan token dan memverifikasi webhook adalah dua
// tanggung jawab berbeda, dan memisahkannya membuat keduanya bisa diuji
// sendiri-sendiri.
type WebhookVerifier interface {
	// ParseWebhook mengembalikan (event, sfuRoom, identity). identity kosong
	// untuk event tingkat-room. Error berarti tanda tangan tidak sah.
	ParseWebhook(authHeader string, body []byte) (event, sfuRoom, identity string, err error)
	Configured() bool
}

// Notifier menyiarkan kejadian panggilan ke topik percakapan.
type Notifier interface {
	Publish(ctx context.Context, topic, eventType string, payload any) error
}

// Service memuat alur bisnis panggilan.
type Service struct {
	repo     Repository
	issuer   TokenIssuer
	webhook  WebhookVerifier
	notifier Notifier
}

// NewService merangkai service.
//
// issuer dan webhook boleh objek yang sama (livekit.Issuer memenuhi keduanya).
func NewService(repo Repository, issuer TokenIssuer, webhook WebhookVerifier, notifier Notifier) *Service {
	return &Service{repo: repo, issuer: issuer, webhook: webhook, notifier: notifier}
}

// SFUReady menandai apakah media server siap.
func (s *Service) SFUReady() bool { return s.issuer.Configured() }

// Start memulai (atau bergabung ke) panggilan pada sebuah percakapan.
//
// Peserta selalu boleh menerbitkan audio/video di panggilan — tidak ada peran
// pendengar seperti di room, jadi canPublish selalu true.
func (s *Service) Start(ctx context.Context, conversationID, userID, identity string, kind Kind) (Session, error) {
	if conversationID == "" || userID == "" {
		return Session{}, ErrInvalidInput
	}
	if !kind.Valid() {
		return Session{}, ErrInvalidInput
	}

	callID := id.New()
	// Id SFU dibuat sama dengan id panggilan supaya tidak ada tabel pemetaan.
	newCallID, sfuRoom, isNew, err := s.repo.Start(ctx, callID, conversationID, string(kind), callID)
	if err != nil {
		return Session{}, err
	}
	if sfuRoom == "" {
		sfuRoom = newCallID
	}

	sess := Session{CallID: newCallID, SFURoomID: sfuRoom, IsNew: isNew}

	if s.issuer.Configured() {
		token, url, err := s.issuer.Issue(sfuRoom, userID, identity, true)
		if err != nil {
			return Session{}, err
		}
		sess.SFUToken, sess.SFUURL = token, url
	}

	// Beri tahu peserta lain bahwa panggilan masuk — inilah yang membuat
	// perangkat mereka berdering. Hanya saat panggilan benar-benar baru.
	if isNew {
		s.notify(ctx, conversationID, EventIncoming, map[string]any{
			"call_id":         newCallID,
			"conversation_id": conversationID,
			"initiator_id":    userID,
			"kind":            string(kind),
		})
	}

	return sess, nil
}

// Answer menjawab panggilan dan menerbitkan token.
func (s *Service) Answer(ctx context.Context, callID, userID, identity, conversationID string) (Session, error) {
	if callID == "" || userID == "" {
		return Session{}, ErrInvalidInput
	}

	sfuRoom, err := s.repo.Answer(ctx, callID)
	if err != nil {
		return Session{}, err
	}
	if sfuRoom == "" {
		sfuRoom = callID
	}

	sess := Session{CallID: callID, SFURoomID: sfuRoom}
	if s.issuer.Configured() {
		token, url, err := s.issuer.Issue(sfuRoom, userID, identity, true)
		if err != nil {
			return Session{}, err
		}
		sess.SFUToken, sess.SFUURL = token, url
	}

	if conversationID != "" {
		s.notify(ctx, conversationID, EventAnswered, map[string]any{
			"call_id": callID, "user_id": userID,
		})
	}
	return sess, nil
}

// Decline menolak panggilan (chat pribadi).
func (s *Service) Decline(ctx context.Context, callID, conversationID string) error {
	if callID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.Decline(ctx, callID); err != nil {
		return err
	}
	if conversationID != "" {
		s.notify(ctx, conversationID, EventEnded, map[string]any{"call_id": callID, "reason": "declined"})
	}
	return nil
}

// Leave meninggalkan panggilan; kalau kosong, panggilan berakhir.
func (s *Service) Leave(ctx context.Context, callID, conversationID string) error {
	if callID == "" {
		return ErrInvalidInput
	}
	if err := s.repo.Leave(ctx, callID); err != nil {
		return err
	}
	if conversationID != "" {
		s.notify(ctx, conversationID, EventEnded, map[string]any{"call_id": callID, "reason": "left"})
	}
	return nil
}

// Active mengembalikan panggilan yang sedang berlangsung pada percakapan.
func (s *Service) Active(ctx context.Context, conversationID string) (*Active, error) {
	if conversationID == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.GetActive(ctx, conversationID)
}

// HandleSFUWebhook memproses satu kiriman webhook dari media server.
//
// Ini jalur yang menutup panggilan ketika perangkat peserta menghilang tanpa
// sempat memanggil leave — aplikasi crash, HP mati, jaringan putus. Tanpa ini,
// baris calls tersangkut 'ongoing' selamanya.
//
// authHeader dan body diteruskan mentah dari request supaya verifikasi tanda
// tangan bekerja atas byte yang persis sama dengan yang dihash LiveKit. Event
// selain participant_left / room_finished diabaikan diam-diam (dibalas sukses,
// supaya LiveKit tidak mengulang kirim).
func (s *Service) HandleSFUWebhook(ctx context.Context, authHeader string, body []byte) error {
	if s.webhook == nil || !s.webhook.Configured() {
		return ErrNoSFU
	}

	event, sfuRoom, identity, err := s.webhook.ParseWebhook(authHeader, body)
	if err != nil {
		return ErrWebhookAuth
	}
	if sfuRoom == "" {
		return nil
	}

	var res *SFUResult
	switch event {
	case SFUParticipantLeftEvent:
		res, err = s.repo.SFUParticipantLeft(ctx, sfuRoom, identity)
	case SFURoomFinishedEvent:
		res, err = s.repo.SFURoomFinished(ctx, sfuRoom)
	default:
		return nil // event lain tidak relevan bagi siklus hidup panggilan
	}
	if err != nil {
		return err
	}

	// Kalau panggilan benar-benar berakhir, beri tahu perangkat lawan bicara
	// supaya UI "sedang menelepon" mereka berhenti sendiri.
	if res != nil && res.Ended && res.ConversationID != "" {
		s.notify(ctx, res.ConversationID, EventEnded, map[string]any{
			"call_id": res.CallID, "reason": "disconnected",
		})
	}
	return nil
}

func (s *Service) notify(ctx context.Context, conversationID, event string, payload any) {
	if s.notifier == nil {
		return
	}
	_ = s.notifier.Publish(ctx, "conversation:"+conversationID, event, payload)
}
