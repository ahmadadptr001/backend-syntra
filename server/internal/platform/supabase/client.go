// Package supabase adalah klien HTTP untuk Supabase online.
//
// Proyek ini TIDAK terhubung langsung ke PostgreSQL. Aksesnya lewat dua API
// HTTP milik Supabase:
//
//	/rest/v1/...      PostgREST — tabel dan fungsi database
//	/auth/v1/...      GoTrue    — identitas pengguna
//
// Konsekuensi yang perlu disadari sejak awal:
//
//  1. Tidak ada transaksi lintas-permintaan. Satu panggilan HTTP = satu
//     transaksi. Operasi yang harus atomik — misalnya menyimpan pesan sambil
//     memperbarui ringkasan percakapan — wajib dibungkus sebagai fungsi
//     database dan dipanggil lewat RPC. Lihat migrations/…_rpc.sql.
//
//  2. Anon key TIDAK memberi hak istimewa apa pun. Ia hanya kunci proyek;
//     yang menentukan baris mana yang boleh disentuh adalah RLS ditambah JWT
//     pengguna yang diteruskan pada tiap permintaan. Karena itu hampir semua
//     panggilan di sini membawa WithToken(<jwt pengguna>).
package supabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	pathREST = "/rest/v1/"
	pathRPC  = "/rest/v1/rpc/"
	PathAuth = "/auth/v1/"

	headerAPIKey        = "apikey"
	headerAuthorization = "Authorization"
	headerPrefer        = "Prefer"
)

// Options adalah kredensial dan setelan transport.
type Options struct {
	// URL proyek, contoh: https://abcdefgh.supabase.co
	URL string

	// AnonKey wajib. Dikirim sebagai header apikey pada setiap permintaan.
	AnonKey string

	// ServiceRoleKey opsional. Kunci ini MELEWATI RLS sepenuhnya, jadi ia
	// hanya boleh dipakai untuk pekerjaan latar yang tidak mewakili pengguna
	// tertentu (job moderasi, migrasi data, backfill). Jangan pernah
	// mengirimkannya ke klien, dan jangan memakainya sebagai jalan pintas
	// ketika sebuah query tertahan RLS — itu menghapus seluruh lapisan
	// keamanan sekaligus.
	ServiceRoleKey string

	Timeout             time.Duration
	MaxIdleConnsPerHost int
}

// Client memanggil API Supabase.
type Client struct {
	baseURL    string
	anonKey    string
	serviceKey string
	httpc      *http.Client
	log        *slog.Logger
}

// New membuat klien dan memvalidasi konfigurasinya.
func New(opts Options, log *slog.Logger) (*Client, error) {
	if opts.URL == "" {
		return nil, errors.New("supabase: SUPABASE_URL wajib diisi")
	}
	if opts.AnonKey == "" {
		return nil, errors.New("supabase: SUPABASE_ANON_KEY wajib diisi")
	}

	parsed, err := url.Parse(opts.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("supabase: SUPABASE_URL tidak valid: %q", opts.URL)
	}
	if parsed.Scheme != "https" {
		log.Warn("supabase: URL bukan https, kredensial akan terkirim tanpa enkripsi", "url", opts.URL)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	maxIdle := opts.MaxIdleConnsPerHost
	if maxIdle <= 0 {
		maxIdle = 50
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = maxIdle

	return &Client{
		baseURL:    strings.TrimRight(opts.URL, "/"),
		anonKey:    opts.AnonKey,
		serviceKey: opts.ServiceRoleKey,
		httpc:      &http.Client{Timeout: timeout, Transport: transport},
		log:        log,
	}, nil
}

// HasServiceRole menandai apakah kunci service tersedia.
func (c *Client) HasServiceRole() bool { return c.serviceKey != "" }

// BaseURL mengembalikan URL proyek tanpa garis miring di akhir.
func (c *Client) BaseURL() string { return c.baseURL }

// AnonKey dibutuhkan pemanggil lain yang menyusun permintaan sendiri
// (misalnya verifier GoTrue di internal/auth).
func (c *Client) AnonKey() string { return c.anonKey }

// HTTPClient mengembalikan http.Client bersama, supaya connection pool-nya
// dipakai ulang alih-alih membuat pool baru per komponen.
func (c *Client) HTTPClient() *http.Client { return c.httpc }

// ---------------------------------------------------------------------------
// Opsi per panggilan
// ---------------------------------------------------------------------------

type callOptions struct {
	token          string
	useServiceRole bool
	prefer         []string
	query          url.Values
}

// Option menyesuaikan satu panggilan.
type Option func(*callOptions)

// WithToken meneruskan JWT pengguna. Inilah yang membuat auth.uid() di sisi
// database terisi, dan karenanya membuat RLS bekerja.
func WithToken(token string) Option {
	return func(o *callOptions) { o.token = token }
}

// WithServiceRole memakai kunci service — melewati RLS. Gunakan sangat hemat.
func WithServiceRole() Option {
	return func(o *callOptions) { o.useServiceRole = true }
}

// WithPrefer menambah nilai header Prefer, misalnya "return=representation".
func WithPrefer(values ...string) Option {
	return func(o *callOptions) { o.prefer = append(o.prefer, values...) }
}

// WithQuery menambahkan query string PostgREST (filter, select, order, limit).
func WithQuery(q url.Values) Option {
	return func(o *callOptions) { o.query = q }
}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

// APIError adalah kegagalan yang dilaporkan Supabase.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details"`
	Hint    string `json:"hint"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "supabase: HTTP %d", e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " [%s]", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, " (hint: %s)", e.Hint)
	}
	return b.String()
}

// IsNotFound menandai baris tunggal yang diminta tidak ada (PGRST116).
func (e *APIError) IsNotFound() bool {
	return e.Status == http.StatusNotFound || e.Code == "PGRST116"
}

// IsDeniedByRLS menandai permintaan tertahan Row Level Security.
//
// Ini error yang paling sering muncul dan paling sering salah didiagnosis.
// Penyebab tersering: JWT pengguna tidak ikut terkirim, sehingga auth.uid()
// bernilai NULL dan seluruh policy gagal. Periksa itu lebih dulu sebelum
// menyalahkan policy-nya.
func (e *APIError) IsDeniedByRLS() bool {
	return e.Status == http.StatusUnauthorized ||
		e.Status == http.StatusForbidden ||
		e.Code == "42501"
}

// ---------------------------------------------------------------------------
// Operasi
// ---------------------------------------------------------------------------

// Select membaca baris dari sebuah tabel atau view.
func (c *Client) Select(ctx context.Context, table string, out any, opts ...Option) error {
	return c.do(ctx, http.MethodGet, pathREST+table, nil, out, opts...)
}

// Insert menambah baris. Sertakan WithPrefer("return=representation") kalau
// hasilnya perlu dibaca kembali.
func (c *Client) Insert(ctx context.Context, table string, body, out any, opts ...Option) error {
	return c.do(ctx, http.MethodPost, pathREST+table, body, out, opts...)
}

// Update mengubah baris yang cocok dengan filter di WithQuery.
func (c *Client) Update(ctx context.Context, table string, body, out any, opts ...Option) error {
	return c.do(ctx, http.MethodPatch, pathREST+table, body, out, opts...)
}

// Delete menghapus baris yang cocok dengan filter di WithQuery.
func (c *Client) Delete(ctx context.Context, table string, opts ...Option) error {
	return c.do(ctx, http.MethodDelete, pathREST+table, nil, nil, opts...)
}

// RPC memanggil fungsi database.
//
// Ini jalur utama repository proyek ini, bukan pengecualian: fungsi database
// adalah satu-satunya cara mendapatkan atomisitas lewat PostgREST, sekaligus
// tempat paling tepat menaruh query yang melibatkan beberapa tabel.
func (c *Client) RPC(ctx context.Context, fn string, args, out any, opts ...Option) error {
	if args == nil {
		args = struct{}{}
	}
	return c.do(ctx, http.MethodPost, pathRPC+fn, args, out, opts...)
}

// Ping memeriksa apakah endpoint REST proyek dapat dihubungi.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+pathREST, nil)
	if err != nil {
		return err
	}
	req.Header.Set(headerAPIKey, c.anonKey)
	req.Header.Set(headerAuthorization, "Bearer "+c.anonKey)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("supabase: tidak dapat dihubungi: %w", err)
	}
	defer drain(resp.Body)

	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("supabase: endpoint REST membalas %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, opts ...Option) error {
	var o callOptions
	for _, apply := range opts {
		apply(&o)
	}

	endpoint := c.baseURL + path
	if len(o.query) > 0 {
		endpoint += "?" + o.query.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("supabase: gagal encode body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("supabase: gagal menyusun permintaan: %w", err)
	}

	// apikey selalu berisi anon key — ia mengidentifikasi proyek, bukan
	// pengguna. Yang mengidentifikasi pengguna adalah header Authorization.
	req.Header.Set(headerAPIKey, c.anonKey)
	req.Header.Set(headerAuthorization, "Bearer "+c.bearer(o))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(o.prefer) > 0 {
		req.Header.Set(headerPrefer, strings.Join(o.prefer, ","))
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("supabase: permintaan gagal: %w", err)
	}
	defer drain(resp.Body)

	if resp.StatusCode >= http.StatusBadRequest {
		return decodeAPIError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // body kosong, misalnya ketika Prefer=return=minimal
		}
		return fmt.Errorf("supabase: gagal membaca respons: %w", err)
	}
	return nil
}

// bearer memilih kredensial untuk header Authorization.
//
// Urutannya disengaja: token pengguna menang atas kunci service. Kalau ada
// JWT pengguna, permintaan ini mewakili orang tersebut dan harus tunduk pada
// RLS-nya — bahkan ketika kunci service kebetulan tersedia.
func (c *Client) bearer(o callOptions) string {
	switch {
	case o.token != "":
		return o.token
	case o.useServiceRole && c.serviceKey != "":
		return c.serviceKey
	default:
		return c.anonKey
	}
}

func decodeAPIError(resp *http.Response) error {
	apiErr := &APIError{Status: resp.StatusCode}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || len(raw) == 0 {
		return apiErr
	}
	if jsonErr := json.Unmarshal(raw, apiErr); jsonErr != nil {
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	return apiErr
}

// drain menghabiskan lalu menutup body supaya koneksi bisa dipakai ulang oleh
// keep-alive. Menutup tanpa membaca sisa body membuat koneksi dibuang.
func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}
