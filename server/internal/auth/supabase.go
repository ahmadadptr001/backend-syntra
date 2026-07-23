package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// SupabaseVerifier memverifikasi JWT lewat endpoint GoTrue milik proyek.
//
// Kenapa memanggil server alih-alih memverifikasi tanda tangan secara lokal:
// dengan modal SUPABASE_URL dan anon key saja, kita belum memegang kunci
// verifikasi apa pun. Endpoint /auth/v1/user menerima token dan mengembalikan
// penggunanya, jadi autentikasi bisa langsung berfungsi tanpa perlu menyalin
// JWT secret ke server ini.
//
// Harganya satu permintaan HTTP per verifikasi, karena itu ada cache. Kalau
// nanti trafiknya naik, gantilah dengan verifikasi tanda tangan lokal memakai
// JWKS proyek (<url>/auth/v1/.well-known/jwks.json) — bentuk Verifier-nya
// tidak berubah, hanya isinya.
type SupabaseVerifier struct {
	baseURL string
	anonKey string
	httpc   *http.Client

	ttl   time.Duration
	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	principal Principal
	expiresAt time.Time
}

// NewSupabaseVerifier membuat verifier.
//
// baseURL, anonKey, dan httpc diambil dari klien Supabase supaya connection
// pool-nya dipakai bersama.
func NewSupabaseVerifier(baseURL, anonKey string, httpc *http.Client, ttl time.Duration) *SupabaseVerifier {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &SupabaseVerifier{
		baseURL: baseURL,
		anonKey: anonKey,
		httpc:   httpc,
		ttl:     ttl,
		cache:   make(map[string]cacheEntry),
	}
}

type supabaseUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Aud   string `json:"aud"`
}

// Verify menukar token dengan identitas pengguna.
func (v *SupabaseVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrNoToken
	}

	key := cacheKey(token)
	if p, ok := v.lookup(key); ok {
		return p, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/auth/v1/user", nil)
	if err != nil {
		return Principal{}, err
	}
	req.Header.Set("apikey", v.anonKey)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := v.httpc.Do(req)
	if err != nil {
		return Principal{}, fmt.Errorf("auth: gagal menghubungi Supabase: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Principal{}, ErrInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return Principal{}, fmt.Errorf("auth: Supabase membalas %d", resp.StatusCode)
	}

	var user supabaseUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return Principal{}, fmt.Errorf("auth: respons Supabase tidak terbaca: %w", err)
	}
	if user.ID == "" {
		return Principal{}, ErrInvalidToken
	}

	principal := Principal{
		UserID: user.ID,
		Scopes: []string{"chat:read", "chat:write"},
		Token:  token,
	}

	v.store(key, principal)
	return principal, nil
}

func (v *SupabaseVerifier) lookup(key string) (Principal, bool) {
	v.mu.RLock()
	entry, ok := v.cache[key]
	v.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return Principal{}, false
	}
	return entry.principal, true
}

func (v *SupabaseVerifier) store(key string, p Principal) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Pembersihan sederhana: saat cache membesar, buang seluruh entri
	// kedaluwarsa. Cukup untuk beban yang ditangani satu instance, dan tidak
	// menambah goroutine latar yang harus ikut dimatikan saat shutdown.
	if len(v.cache) > 4096 {
		now := time.Now()
		for k, e := range v.cache {
			if now.After(e.expiresAt) {
				delete(v.cache, k)
			}
		}
	}

	v.cache[key] = cacheEntry{principal: p, expiresAt: time.Now().Add(v.ttl)}
}

// cacheKey membuat kunci dari hash token, bukan tokennya sendiri, supaya
// kredensial tidak tersimpan apa adanya di memori sebagai kunci map.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
