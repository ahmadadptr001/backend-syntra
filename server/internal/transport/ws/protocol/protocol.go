// Package protocol mendefinisikan format frame WebSocket.
//
// Seluruh frame — dua arah — memakai satu amplop JSON yang sama. Satu bentuk
// untuk semua membuat klien Kotlin cukup punya satu decoder dan satu titik
// dispatch, alih-alih menebak bentuk pesan dari isinya.
//
//	{"type":"message.send","ref":"c-42","data":{...}}   klien -> server
//	{"type":"ack","ref":"c-42","data":{...},"ts":...}   server -> klien
//	{"type":"error","ref":"c-42","error":{...}}         server -> klien
//
// Kontrak lengkapnya ada di api/asyncapi.yaml.
package protocol

import (
	"encoding/json"
	"time"
)

// Tipe frame dari klien ke server.
const (
	TypeSubscribe   = "subscribe"
	TypeUnsubscribe = "unsubscribe"
	TypePing        = "ping"
	TypeMessageSend = "message.send"
	TypeMessageRead = "message.read"
	// TypeMessageDelivered dikirim penerima saat pesan sampai di perangkatnya
	// (baik sedang membuka chat maupun hanya di daftar chat) supaya pengirim
	// bisa menaikkan centang dari 1 (terkirim ke server) menjadi 2 (sampai ke
	// perangkat lawan). EFEMERAL — tidak pernah disimpan, hanya disiarkan.
	TypeMessageDelivered = "message.delivered"
	TypeTypingStart      = "typing.start"
	TypeTypingStop       = "typing.stop"
	TypePresenceQuery    = "presence.query"

	// TypeRoomChat adalah pesan teks di dalam voice room.
	//
	// EFEMERAL DENGAN SENGAJA: pesan ini tidak pernah disimpan. Ia disiarkan
	// ke peserta yang sedang terhubung, lalu hilang. Begitu room berakhir,
	// seluruh percakapannya lenyap — tidak ada riwayat untuk dimuat, dan tidak
	// ada endpoint untuk mengambilnya kembali.
	TypeRoomChat = "room.chat"

	// TypeLiveComment adalah komentar di dalam siaran langsung (live). Sama
	// efemeralnya dengan room.chat: tidak pernah disimpan, hanya disiarkan ke
	// penonton yang sedang terhubung, lalu hilang bersama siarannya.
	TypeLiveComment = "live.comment"
)

// Catatan: masuk dan keluar voice room dilakukan lewat REST
// (POST /rooms/{id}/join dan /leave), bukan lewat frame. Konstanta
// "room.join"/"room.leave" pernah ada di sini tanpa handler, sehingga
// mengirimnya hanya menghasilkan "unknown_type" — menyesatkan, jadi dibuang.

// Tipe frame dari server ke klien.
const (
	TypeReady          = "ready"
	TypeAck            = "ack"
	TypeError          = "error"
	TypePong           = "pong"
	TypePresenceUpdate = "presence.update"

	// TypeRoomMessage adalah siaran pesan room ke peserta lain. Efemeral —
	// lihat catatan di TypeRoomChat.
	TypeRoomMessage = "room.message"

	// TypeLiveMessage adalah siaran komentar live ke penonton lain. Efemeral —
	// lihat catatan di TypeLiveComment.
	TypeLiveMessage = "live.message"

	TypeNotification = "notification.new"
)

// Kode error yang dikenal klien. Klien memutuskan tindakan berdasarkan kode
// ini, bukan berdasarkan teks pesan — teksnya boleh berubah kapan saja.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeRateLimited  = "rate_limited"
	CodeUnknownType  = "unknown_type"
	CodeInternal     = "internal"
)

// Envelope adalah satu frame WebSocket.
type Envelope struct {
	Type string `json:"type"`

	// Ref adalah korelasi opsional yang ditentukan klien. Klien memakainya
	// untuk mencocokkan balasan dengan permintaan, sehingga pesan optimistik
	// di UI bisa diganti dengan pesan asli begitu ack tiba.
	Ref string `json:"ref,omitempty"`

	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
	TS    int64           `json:"ts,omitempty"`
}

// Error adalah detail kegagalan sebuah frame.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DecodeData membaca isi field data ke struct tujuan.
func (e Envelope) DecodeData(dst any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, dst)
}

// NewEvent membangun frame berisi payload.
func NewEvent(eventType string, payload any) (Envelope, error) {
	env := Envelope{
		Type: eventType,
		TS:   time.Now().UnixMilli(),
	}

	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		env.Data = raw
	}

	return env, nil
}

// NewAck membangun balasan sukses untuk sebuah ref.
func NewAck(ref string, payload any) (Envelope, error) {
	env, err := NewEvent(TypeAck, payload)
	if err != nil {
		return Envelope{}, err
	}
	env.Ref = ref
	return env, nil
}

// NewError membangun frame kegagalan.
func NewError(ref, code, message string) Envelope {
	return Envelope{
		Type:  TypeError,
		Ref:   ref,
		Error: &Error{Code: code, Message: message},
		TS:    time.Now().UnixMilli(),
	}
}

// Encode mengubah envelope menjadi byte yang siap dikirim.
func Encode(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

// EncodeEvent adalah gabungan NewEvent + Encode, bentuk yang paling sering dipakai.
func EncodeEvent(eventType string, payload any) ([]byte, error) {
	env, err := NewEvent(eventType, payload)
	if err != nil {
		return nil, err
	}
	return Encode(env)
}
