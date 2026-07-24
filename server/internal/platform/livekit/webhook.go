package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Webhook LiveKit: bagaimana ia diautentikasi, dan kenapa perlu.
//
// LiveKit Cloud memanggil balik backend setiap ada kejadian di room (peserta
// keluar, room selesai). Endpoint penerima harus publik agar LiveKit bisa
// menjangkaunya — jadi ia tidak bisa dilindungi JWT pengguna. Sebagai gantinya
// LiveKit menandatangani setiap kiriman: header Authorization berisi sebuah JWT
// HS256 yang ditandatangani dengan API secret yang sama, dan JWT itu memuat
// klaim `sha256` — hash SHA-256 dari body dalam base64.
//
// Verifikasi karena itu dua lapis:
//  1. Tanda tangan JWT valid terhadap API secret → kiriman benar dari LiveKit.
//  2. Hash body cocok dengan klaim `sha256` → body tidak diubah di jalan.
//
// Tanpa kedua pemeriksaan ini, siapa pun yang tahu URL webhook bisa memaksa
// panggilan orang lain berakhir. Dengan keduanya, hanya LiveKit yang bisa.

// webhookLeeway memberi toleransi selisih jam saat memeriksa kedaluwarsa token.
const webhookLeeway = 60 * time.Second

// WebhookEvent adalah bagian dari payload webhook LiveKit yang dipakai backend.
//
// LiveKit mengirim lebih banyak field (statistik track, region, dll); hanya
// yang relevan untuk siklus hidup panggilan yang diurai di sini.
type WebhookEvent struct {
	Event string `json:"event"`
	Room  struct {
		Name string `json:"name"`
	} `json:"room"`
	Participant struct {
		Identity string `json:"identity"`
	} `json:"participant"`
}

// ErrWebhookAuth menandai tanda tangan webhook tidak sah — kiriman ditolak.
var ErrWebhookAuth = errors.New("livekit: tanda tangan webhook tidak sah")

// webhookClaims adalah klaim JWT yang menyertai setiap webhook.
type webhookClaims struct {
	Issuer    string `json:"iss"`
	SHA256    string `json:"sha256"`
	NotBefore int64  `json:"nbf"`
	NotAfter  int64  `json:"exp"`
}

// ParseWebhook memverifikasi tanda tangan lalu mengurai payload webhook.
//
// authHeader adalah nilai mentah header Authorization; body adalah byte mentah
// request — persis seperti yang dihash LiveKit, jadi ia WAJIB dibaca sebelum
// di-decode JSON. Mengembalikan ErrWebhookAuth kalau verifikasi gagal.
//
// Bentuk keluarannya (event, sfuRoom, identity) sengaja cocok dengan port
// call.WebhookVerifier supaya Issuer memenuhinya tanpa adapter.
func (i *Issuer) ParseWebhook(authHeader string, body []byte) (event, sfuRoom, identity string, err error) {
	if !i.Configured() {
		return "", "", "", errors.New("livekit: kredensial belum dikonfigurasi")
	}

	token := strings.TrimSpace(authHeader)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", "", ErrWebhookAuth
	}

	claims, err := i.verifyWebhookToken(token)
	if err != nil {
		return "", "", "", err
	}

	// Body harus cocok dengan hash yang ditandatangani. base64 standar (bukan
	// url-safe) — itu yang dipakai LiveKit untuk klaim sha256.
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(claims.SHA256)) != 1 {
		return "", "", "", ErrWebhookAuth
	}

	var parsed WebhookEvent
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", "", fmt.Errorf("livekit: payload webhook tidak valid: %w", err)
	}
	return parsed.Event, parsed.Room.Name, parsed.Participant.Identity, nil
}

// verifyWebhookToken memeriksa tanda tangan HS256 dan klaim dasar JWT webhook.
func (i *Issuer) verifyWebhookToken(token string) (*webhookClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrWebhookAuth
	}

	// Tanda tangan menutupi "header.payload" apa adanya.
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(i.apiSecret))
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrWebhookAuth
	}
	if subtle.ConstantTimeCompare(expectedSig, gotSig) != 1 {
		return nil, ErrWebhookAuth
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrWebhookAuth
	}
	var claims webhookClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, ErrWebhookAuth
	}

	// Token harus diterbitkan oleh API key kita, bukan proyek lain.
	if claims.Issuer != i.apiKey {
		return nil, ErrWebhookAuth
	}
	// Tolak token kedaluwarsa (dengan toleransi jam). exp/nbf boleh 0 kalau
	// LiveKit tidak menyertakannya — dalam hal itu tidak diperiksa.
	now := time.Now()
	if claims.NotAfter != 0 && now.After(time.Unix(claims.NotAfter, 0).Add(webhookLeeway)) {
		return nil, ErrWebhookAuth
	}
	if claims.NotBefore != 0 && now.Before(time.Unix(claims.NotBefore, 0).Add(-webhookLeeway)) {
		return nil, ErrWebhookAuth
	}

	return &claims, nil
}
