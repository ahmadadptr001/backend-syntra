package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// signWebhook meniru cara LiveKit menandatangani webhook: JWT HS256 dengan klaim
// sha256 berisi hash body dalam base64 standar.
func signWebhook(t *testing.T, apiKey, apiSecret string, body []byte, exp int64) string {
	t.Helper()

	sum := sha256.Sum256(body)
	claims := map[string]any{
		"iss":    apiKey,
		"sha256": base64.StdEncoding.EncodeToString(sum[:]),
	}
	if exp != 0 {
		claims["exp"] = exp
	}

	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signingInput := encode(header) + "." + encode(payload)

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + encode(mac.Sum(nil))
}

func TestParseWebhook(t *testing.T) {
	const key, secret = "APIkey123", "secret-value-xyz"
	iss := New(key, secret, "wss://x.livekit.cloud")

	body := []byte(`{"event":"participant_left","room":{"name":"call-abc"},"participant":{"identity":"user-uuid-1"}}`)
	token := signWebhook(t, key, secret, body, time.Now().Add(time.Hour).Unix())

	event, room, identity, err := iss.ParseWebhook("Bearer "+token, body)
	if err != nil {
		t.Fatalf("webhook sah ditolak: %v", err)
	}
	if event != "participant_left" || room != "call-abc" || identity != "user-uuid-1" {
		t.Fatalf("hasil urai salah: event=%q room=%q identity=%q", event, room, identity)
	}
}

func TestParseWebhookRejectsTampering(t *testing.T) {
	const key, secret = "APIkey123", "secret-value-xyz"
	iss := New(key, secret, "wss://x.livekit.cloud")

	body := []byte(`{"event":"room_finished","room":{"name":"call-abc"}}`)
	token := signWebhook(t, key, secret, body, time.Now().Add(time.Hour).Unix())

	cases := []struct {
		name   string
		header string
		body   []byte
	}{
		{"body diubah setelah ditandatangani", "Bearer " + token, append([]byte(nil), []byte(`{"event":"room_finished","room":{"name":"call-XXX"}}`)...)},
		{"ditandatangani secret lain", "Bearer " + signWebhook(t, key, "secret-lain", body, time.Now().Add(time.Hour).Unix()), body},
		{"issuer lain", "Bearer " + signWebhook(t, "key-lain", secret, body, time.Now().Add(time.Hour).Unix()), body},
		{"token kedaluwarsa", "Bearer " + signWebhook(t, key, secret, body, time.Now().Add(-time.Hour).Unix()), body},
		{"header kosong", "", body},
		{"token bukan JWT", "Bearer bukan.jwt", body},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := iss.ParseWebhook(tc.header, tc.body); err == nil {
				t.Fatal("webhook tidak sah malah diterima")
			}
		})
	}
}
