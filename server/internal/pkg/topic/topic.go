// Package topic menyeragamkan penamaan kanal pub/sub.
//
// Nama topik dipakai di tiga tempat: Hub (routing lokal), Redis (fanout antar
// instance), dan service domain (menentukan tujuan siaran). Kalau formatnya
// ditulis manual di masing-masing tempat, satu salah ketik berarti pesan
// hilang tanpa error — jenis bug yang paling sulit dilacak. Package ini
// membuat format itu punya satu sumber kebenaran.
package topic

import "strings"

// Kind adalah jenis entitas yang diwakili sebuah topik.
type Kind string

// Jenis topik yang dikenal sistem.
const (
	KindUser         Kind = "user"
	KindConversation Kind = "conversation"
	KindRoom         Kind = "room"
	KindReel         Kind = "reel"
)

const separator = ":"

// User adalah kanal pribadi satu pengguna: notifikasi, badge, undangan.
// Seluruh perangkat milik pengguna tersebut ikut mendengarkan kanal ini.
func User(userID string) string { return string(KindUser) + separator + userID }

// Conversation adalah kanal satu percakapan chat.
func Conversation(conversationID string) string {
	return string(KindConversation) + separator + conversationID
}

// Room adalah kanal satu voice room.
func Room(roomID string) string { return string(KindRoom) + separator + roomID }

// Reel adalah kanal satu reel, untuk counter like/komentar secara langsung.
func Reel(reelID string) string { return string(KindReel) + separator + reelID }

// Parse memecah topik menjadi jenis dan id.
//
// Dibutuhkan lapisan otorisasi: sebelum sebuah klien boleh berlangganan,
// server harus tahu entitas apa yang sedang ia minta.
func Parse(name string) (Kind, string, bool) {
	kind, id, found := strings.Cut(name, separator)
	if !found || id == "" {
		return "", "", false
	}

	switch Kind(kind) {
	case KindUser, KindConversation, KindRoom, KindReel:
		return Kind(kind), id, true
	default:
		return "", "", false
	}
}
