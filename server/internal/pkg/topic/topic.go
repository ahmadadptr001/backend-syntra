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

	// Feed global (singleton): tidak terikat satu entitas, melainkan kanal umum
	// yang dilanggan semua pengguna terautentikasi saat tab terkait terbuka.
	KindRoomsFeed Kind = "rooms"
	KindReelsFeed Kind = "reels"
)

// feedID adalah id tetap kanal feed global — hanya ada satu per jenis, jadi
// nilainya sekadar penggenap format "kind:id".
const feedID = "all"

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

// RoomsFeed adalah kanal feed voice room global: dipakai untuk mengumumkan room
// baru (room.created) ke siapa pun yang sedang membuka tab Rooms.
func RoomsFeed() string { return string(KindRoomsFeed) + separator + feedID }

// ReelsFeed adalah kanal feed reels/shorts global: mengumumkan reel baru/terhapus
// (reel.new / reel.deleted) ke siapa pun yang sedang membuka tab Shorts.
func ReelsFeed() string { return string(KindReelsFeed) + separator + feedID }

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
	case KindUser, KindConversation, KindRoom, KindReel, KindRoomsFeed, KindReelsFeed:
		return Kind(kind), id, true
	default:
		return "", "", false
	}
}
