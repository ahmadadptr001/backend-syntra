// Package id membuat identifier UUIDv7.
//
// v7 dipilih karena bagian depannya adalah timestamp milidetik, sehingga
// identifier terurut secara waktu. Konsekuensinya di database: index B-tree
// menulis di ujung kanan (bukan acak seperti v4), dan pagination pesan bisa
// memakai primary key langsung tanpa index tambahan pada created_at.
//
// Lihat docs/erd.md bagian "Konvensi".
package id

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New mengembalikan UUIDv7 dalam bentuk string berformat kanonik.
func New() string {
	return encode(newBytes())
}

// NewAt sama seperti New tetapi memakai waktu tertentu. Berguna untuk test.
func NewAt(t time.Time) string {
	b := newBytes()
	writeTimestamp(&b, t)
	return encode(b)
}

func newBytes() [16]byte {
	var b [16]byte
	writeTimestamp(&b, time.Now())

	// 74 bit sisanya diisi acak.
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand tidak boleh gagal; kalau gagal, proses memang tidak layak lanjut.
		panic("id: sumber acak tidak tersedia: " + err.Error())
	}

	b[6] = (b[6] & 0x0f) | 0x70 // versi 7
	b[8] = (b[8] & 0x3f) | 0x80 // varian RFC 4122
	return b
}

func writeTimestamp(b *[16]byte, t time.Time) {
	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
}

func encode(b [16]byte) string {
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
