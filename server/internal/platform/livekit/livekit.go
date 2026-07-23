// Package livekit menerbitkan token akses untuk media server LiveKit.
//
// Token LiveKit adalah JWT HS256 yang ditandatangani dengan API secret, berisi
// klaim `video` yang menyatakan room mana yang boleh dimasuki dan apakah
// pemegangnya boleh menerbitkan audio.
//
// Ditulis sendiri, bukan memakai SDK LiveKit, karena yang dibutuhkan hanya
// satu fungsi penandatanganan — SDK-nya membawa puluhan dependensi untuk
// server API yang tidak dipakai sama sekali di sini.
//
// Kenapa token ini penting: ia satu-satunya hal yang mencegah orang yang tidak
// berhak ikut mendengarkan, dan mencegah pendengar biasa menyalakan mikrofon.
// Otorisasi di UI saja tidak berarti apa-apa — SFU-lah yang menegakkannya.
package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenTTL adalah umur token akses.
//
// Pendek dengan sengaja: token yang bocor hanya berguna sebentar. Klien
// meminta token baru setiap kali bergabung, jadi ini tidak merepotkan.
const TokenTTL = 6 * time.Hour

// Issuer menerbitkan token LiveKit.
type Issuer struct {
	apiKey    string
	apiSecret string
	url       string
}

// New membuat issuer. Nilai kosong berarti SFU belum dikonfigurasi — issuer
// tetap bisa dibuat supaya aplikasi berjalan, tetapi Configured() akan false.
func New(apiKey, apiSecret, url string) *Issuer {
	return &Issuer{
		apiKey:    strings.TrimSpace(apiKey),
		apiSecret: strings.TrimSpace(apiSecret),
		url:       strings.TrimSpace(url),
	}
}

// Configured menandai apakah kredensial lengkap.
func (i *Issuer) Configured() bool {
	return i.apiKey != "" && i.apiSecret != "" && i.url != ""
}

// videoGrant adalah izin yang ditegakkan LiveKit di sisi server.
type videoGrant struct {
	Room         string `json:"room"`
	RoomJoin     bool   `json:"roomJoin"`
	CanPublish   bool   `json:"canPublish"`
	CanSubscribe bool   `json:"canSubscribe"`

	// canPublishData dipakai untuk pesan realtime di dalam room (misalnya
	// reaksi emoji). Dibiarkan menyala karena tidak membawa risiko audio.
	CanPublishData bool `json:"canPublishData"`
}

type claims struct {
	Issuer    string     `json:"iss"`
	Subject   string     `json:"sub"`
	Name      string     `json:"name,omitempty"`
	NotAfter  int64      `json:"exp"`
	NotBefore int64      `json:"nbf"`
	Video     videoGrant `json:"video"`
}

// Issue menerbitkan token untuk satu orang di satu room.
//
// canPublish=false menghasilkan token yang secara teknis tidak bisa
// menerbitkan audio. Inilah yang membedakan pendengar dari pembicara — bukan
// tombol yang disembunyikan di aplikasi.
func (i *Issuer) Issue(roomID, userID, identity string, canPublish bool) (string, string, error) {
	if !i.Configured() {
		return "", "", errors.New("livekit: LIVEKIT_API_KEY, LIVEKIT_API_SECRET, dan LIVEKIT_URL wajib diisi")
	}
	if roomID == "" || userID == "" {
		return "", "", errors.New("livekit: roomID dan userID wajib diisi")
	}

	now := time.Now()
	payload := claims{
		Issuer:    i.apiKey,
		Subject:   userID,
		Name:      identity,
		NotBefore: now.Add(-30 * time.Second).Unix(), // toleransi selisih jam
		NotAfter:  now.Add(TokenTTL).Unix(),
		Video: videoGrant{
			Room:           roomID,
			RoomJoin:       true,
			CanPublish:     canPublish,
			CanSubscribe:   true,
			CanPublishData: true,
		},
	}

	token, err := i.sign(payload)
	if err != nil {
		return "", "", err
	}
	return token, i.url, nil
}

func (i *Issuer) sign(payload claims) (string, error) {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("livekit: gagal encode header: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("livekit: gagal encode payload: %w", err)
	}

	unsigned := encode(headerJSON) + "." + encode(payloadJSON)

	mac := hmac.New(sha256.New, []byte(i.apiSecret))
	mac.Write([]byte(unsigned))

	return unsigned + "." + encode(mac.Sum(nil)), nil
}

// encode memakai base64url tanpa padding, sesuai spesifikasi JWT.
func encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
