package ws

import (
	"context"

	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws/protocol"
)

// Publisher membuat Hub memenuhi port Publisher milik domain.
//
// Arah dependensinya patut diperhatikan: domain mendeklarasikan interface
// Publisher, dan transport-lah yang menyesuaikan diri. Bukan sebaliknya.
// Berkat itu domain chat tidak pernah menyebut WebSocket, dan menambahkan
// kanal kedua (misalnya push notification untuk perangkat yang sedang offline)
// nanti cukup dilakukan dengan membungkus port yang sama.
type Publisher struct {
	hub *Hub
}

// NewPublisher membungkus hub sebagai publisher domain.
func NewPublisher(hub *Hub) *Publisher {
	return &Publisher{hub: hub}
}

// Publish membungkus payload ke dalam amplop protokol lalu menyiarkannya ke
// seluruh instance.
func (p *Publisher) Publish(ctx context.Context, name, eventType string, payload any) error {
	frame, err := protocol.EncodeEvent(eventType, payload)
	if err != nil {
		return err
	}
	return p.hub.Publish(ctx, name, frame)
}
