// Package config memuat seluruh konfigurasi runtime dari environment variable.
//
// Aturan: hanya package ini yang boleh membaca os.Getenv. Package lain menerima
// nilai yang sudah divalidasi lewat struct, sehingga konfigurasi yang salah
// gagal saat startup, bukan saat request pertama masuk.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config adalah akar seluruh konfigurasi aplikasi.
type Config struct {
	Env     string
	Version string

	// EnvFile mencatat berkas .env yang dipakai, atau kosong kalau seluruh
	// konfigurasi datang dari environment proses. Berguna saat men-debug
	// "kenapa nilainya bukan yang saya kira".
	EnvFile string

	HTTP      HTTP
	WS        WS
	Supabase  Supabase
	Redis     Redis
	Auth      Auth
	LiveKit   LiveKit
	RateLimit RateLimit
	Log       Log
}

// RateLimit membatasi jumlah permintaan REST per pengguna.
//
// Melengkapi batas per-IP di nginx: satu IP bisa menaungi banyak pengguna
// (NAT kampus/kantor), dan satu pengguna bisa berpindah IP. Batas per-pengguna
// menegakkan keadilan pada tingkat identitas, bukan alamat jaringan.
type RateLimit struct {
	// PerMinute adalah jumlah permintaan maksimum per pengguna per menit.
	// <= 0 mematikan rate limiting per pengguna sepenuhnya.
	PerMinute int
}

// HTTP mengatur listener REST.
type HTTP struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	CORSOrigins     []string
}

// WS mengatur endpoint WebSocket. Nilainya sengaja dipisah dari HTTP karena
// koneksi socket berumur panjang dan tidak boleh terkena WriteTimeout REST.
type WS struct {
	Path           string
	ReadLimitBytes int64
	WriteWait      time.Duration
	PongWait       time.Duration
	PingInterval   time.Duration
	SendBuffer     int
	AllowedOrigins []string

	// PresenceTTL harus lebih besar dari PingInterval. Kalau lebih kecil,
	// pengguna yang koneksinya sehat akan berkedip offline di sela dua ping.
	PresenceTTL time.Duration
}

// Supabase mengatur akses ke proyek Supabase online.
//
// Tidak ada DSN PostgreSQL di sini: aplikasi berbicara lewat API HTTP
// Supabase (PostgREST + GoTrue), bukan koneksi database langsung.
type Supabase struct {
	// URL proyek, contoh https://abcdefgh.supabase.co
	URL string

	// AnonKey wajib. Kunci publik proyek — ia mengidentifikasi proyek, bukan
	// pengguna, dan tidak memberi hak akses apa pun dengan sendirinya.
	AnonKey string

	// ServiceRoleKey opsional dan MELEWATI RLS sepenuhnya. Isi hanya kalau
	// benar-benar ada pekerjaan latar yang tidak mewakili pengguna tertentu.
	ServiceRoleKey string

	// StorageBucket menampung media yang diunggah klien.
	StorageBucket string

	// MediaCDNBase, kalau diisi, mengubah URL BACA media agar melewati CDN
	// (mis. Cloudflare) alih-alih langsung dari Supabase Storage — memangkas
	// egress Supabase (Cloudflare men-cache tiap objek di edge, gratis).
	// Contoh: https://cdn.syntra.fun. Unggah & hapus tetap ke Supabase. Kosong =
	// pakai URL publik Supabase seperti biasa.
	MediaCDNBase string

	Timeout time.Duration

	// AuthCacheTTL menentukan berapa lama hasil verifikasi token disimpan,
	// supaya tidak setiap request memanggil /auth/v1/user.
	AuthCacheTTL time.Duration
}

// Redis dipakai untuk presence, cache, rate limit, dan fanout antar-instance.
type Redis struct {
	URL string
}

// Auth mengatur verifikasi token.
//
// Tidak ada setelan issuer atau kunci publik: verifikasi dilakukan dengan
// menanyakan token ke endpoint GoTrue proyek, sehingga tidak ada material
// kunci yang perlu disalin ke server ini. Lihat internal/auth/supabase.go.
type Auth struct {
	DevBypass bool
}

// LiveKit adalah kredensial media server untuk voice room.
//
// Backend hanya menerbitkan token; audio mengalir langsung antara klien dan
// media server. Kalau ketiganya kosong, voice room tetap bisa dibuat dan
// didaftar, tetapi tidak akan ada suara sama sekali.
type LiveKit struct {
	APIKey    string
	APISecret string

	// URL yang dihubungi klien, contoh wss://xxx.livekit.cloud
	URL string
}

// Log mengatur output slog.
type Log struct {
	Level  string
	Format string
}

// IsProduction menandai environment yang tidak boleh memakai jalur pintas development.
func (c *Config) IsProduction() bool {
	return c.Env == "production" || c.Env == "staging"
}

// Load membaca environment dan mengembalikan konfigurasi yang sudah tervalidasi.
//
// Berkas .env dimuat lebih dulu kalau ada, tetapi tidak pernah menimpa
// environment variable yang sudah diset. Urutan prioritasnya:
//
//	environment proses  >  berkas .env  >  nilai bawaan di bawah
func Load() (*Config, error) {
	envFile := loadDotEnv(".env")

	l := &loader{}

	cfg := &Config{
		Env:     l.str("APP_ENV", "development"),
		Version: l.str("APP_VERSION", "dev"),
		EnvFile: envFile,
		HTTP: HTTP{
			Addr:            l.str("HTTP_ADDR", ":8080"),
			ReadTimeout:     l.dur("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    l.dur("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     l.dur("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: l.dur("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			CORSOrigins:     l.csv("HTTP_CORS_ORIGINS", nil),
		},
		WS: WS{
			Path:           l.str("WS_PATH", "/api/v1/ws"),
			ReadLimitBytes: int64(l.num("WS_READ_LIMIT_BYTES", 64*1024)),
			WriteWait:      l.dur("WS_WRITE_WAIT", 10*time.Second),
			PongWait:       l.dur("WS_PONG_WAIT", 60*time.Second),
			PingInterval:   l.dur("WS_PING_INTERVAL", 25*time.Second),
			SendBuffer:     l.num("WS_SEND_BUFFER", 64),
			AllowedOrigins: l.csv("WS_ALLOWED_ORIGINS", nil),
			PresenceTTL:    l.dur("WS_PRESENCE_TTL", 90*time.Second),
		},
		Supabase: Supabase{
			URL:            l.str("SUPABASE_URL", ""),
			AnonKey:        l.str("SUPABASE_ANON_KEY", ""),
			ServiceRoleKey: l.str("SUPABASE_SERVICE_ROLE_KEY", ""),
			StorageBucket:  l.str("SUPABASE_STORAGE_BUCKET", "media"),
			MediaCDNBase:   l.str("SUPABASE_MEDIA_CDN_BASE", ""),
			Timeout:        l.dur("SUPABASE_TIMEOUT", 10*time.Second),
			AuthCacheTTL:   l.dur("SUPABASE_AUTH_CACHE_TTL", time.Minute),
		},
		Redis: Redis{
			URL: l.str("REDIS_URL", ""),
		},
		Auth: Auth{
			DevBypass: l.boolean("AUTH_DEV_BYPASS", false),
		},
		LiveKit: LiveKit{
			APIKey:    l.str("LIVEKIT_API_KEY", ""),
			APISecret: l.str("LIVEKIT_API_SECRET", ""),
			URL:       l.str("LIVEKIT_URL", ""),
		},
		RateLimit: RateLimit{
			PerMinute: l.num("RATE_LIMIT_PER_MINUTE", 240),
		},
		Log: Log{
			Level:  l.str("LOG_LEVEL", "info"),
			Format: l.str("LOG_FORMAT", "json"),
		},
	}

	if len(l.errs) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(l.errs, "; "))
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var problems []string

	if c.Supabase.URL == "" {
		problems = append(problems, "SUPABASE_URL wajib diisi")
	}
	if c.Supabase.AnonKey == "" {
		problems = append(problems, "SUPABASE_ANON_KEY wajib diisi")
	}
	if c.Redis.URL == "" {
		problems = append(problems, "REDIS_URL wajib diisi")
	}
	if c.WS.PingInterval >= c.WS.PongWait {
		problems = append(problems,
			"WS_PING_INTERVAL harus lebih kecil dari WS_PONG_WAIT, kalau tidak koneksi sehat akan diputus")
	}
	if c.WS.PresenceTTL <= c.WS.PingInterval {
		problems = append(problems,
			"WS_PRESENCE_TTL harus lebih besar dari WS_PING_INTERVAL, kalau tidak pengguna online akan berkedip offline")
	}
	// Jalur pintas development tidak boleh ikut terbawa ke lingkungan nyata.
	if c.IsProduction() && c.Auth.DevBypass {
		problems = append(problems, "AUTH_DEV_BYPASS harus false ketika APP_ENV="+c.Env)
	}
	if c.IsProduction() && len(c.HTTP.CORSOrigins) == 0 {
		problems = append(problems, "HTTP_CORS_ORIGINS wajib eksplisit di production")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}

// loader mengumpulkan seluruh error parsing supaya pengguna melihat semua
// masalah konfigurasi sekaligus, bukan satu per satu tiap kali restart.
type loader struct {
	errs []string
}

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) num(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s bukan angka yang valid (%q)", key, raw))
		return def
	}
	return v
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s bukan durasi yang valid (%q), contoh: 30s, 5m, 1h", key, raw))
		return def
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s bukan boolean yang valid (%q)", key, raw))
		return def
	}
	return v
}

func (l *loader) csv(key string, def []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
