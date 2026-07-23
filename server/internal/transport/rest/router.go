// Package rest merangkai seluruh rute HTTP.
//
// Package ini bernama "rest", bukan "http", karena package bernama http yang
// juga mengimpor net/http memaksa alias di setiap berkas — bentuk kerapian
// yang justru bikin repot.
//
// Router memakai net/http.ServeMux bawaan Go 1.22+, yang sudah mendukung pola
// berbasis method dan wildcard path ("POST /api/v1/conversations/{id}/messages").
// Untuk kebutuhan sebesar ini, router pihak ketiga tidak menambah apa pun
// selain satu dependensi.
package rest

import (
	"log/slog"
	"net/http"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/handler"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/httpx"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
)

// Deps adalah segala sesuatu yang dibutuhkan router. Dirakit di internal/app.
type Deps struct {
	Log              *slog.Logger
	Verifier         auth.Verifier
	CORSOrigins      []string
	AllowDebugHeader bool

	WSPath    string
	WSHandler http.Handler

	Health  *handler.Health
	Account *handler.Account
	Chat    *handler.Chat
	Story   *handler.Story
	User    *handler.User
	Media   *handler.Media
	Room    *handler.Room
}

// NewRouter membangun handler HTTP lengkap dengan middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// --- publik: tanpa autentikasi ---
	mux.HandleFunc("GET /healthz", d.Health.Live)
	mux.HandleFunc("GET /readyz", d.Health.Ready)

	// Endpoint auth berada di luar middleware auth — justru inilah yang
	// menerbitkan tokennya. Logout memeriksa header sendiri.
	mux.HandleFunc("POST /api/v1/auth/register", d.Account.Register)
	mux.HandleFunc("POST /api/v1/auth/login", d.Account.Login)
	mux.HandleFunc("POST /api/v1/auth/refresh", d.Account.Refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", d.Account.Logout)

	// --- REST terproteksi ---
	protected := middleware.Auth(d.Verifier, middleware.AuthOptions{
		AllowDebugHeader: d.AllowDebugHeader,
	})

	// --- percakapan ---
	mux.Handle("GET /api/v1/conversations",
		protected(http.HandlerFunc(d.Chat.ListConversations)))
	mux.Handle("POST /api/v1/conversations",
		protected(http.HandlerFunc(d.Chat.CreateConversation)))
	mux.Handle("GET /api/v1/conversations/{id}/messages",
		protected(http.HandlerFunc(d.Chat.ListMessages)))
	mux.Handle("POST /api/v1/conversations/{id}/messages",
		protected(http.HandlerFunc(d.Chat.SendMessage)))

	// --- story ---
	mux.Handle("GET /api/v1/stories",
		protected(http.HandlerFunc(d.Story.List)))
	mux.Handle("POST /api/v1/stories",
		protected(http.HandlerFunc(d.Story.Create)))
	mux.Handle("POST /api/v1/stories/{id}/view",
		protected(http.HandlerFunc(d.Story.MarkViewed)))

	// --- direktori pengguna & graf pertemanan ---
	//
	// Pola "me/following" didaftarkan lebih dulu daripada "{username}".
	// ServeMux Go 1.22 memilih pola yang lebih spesifik, jadi urutan penulisan
	// sebenarnya tidak menentukan — tapi menulisnya begini membuat maksudnya
	// terbaca oleh manusia.
	mux.Handle("GET /api/v1/users/me/following",
		protected(http.HandlerFunc(d.User.ListFollowing)))
	mux.Handle("GET /api/v1/users/{username}",
		protected(http.HandlerFunc(d.User.GetByUsername)))
	mux.Handle("POST /api/v1/users/{username}/follow",
		protected(http.HandlerFunc(d.User.Follow)))
	mux.Handle("DELETE /api/v1/users/{username}/follow",
		protected(http.HandlerFunc(d.User.Unfollow)))

	// --- voice room ---
	mux.Handle("GET /api/v1/rooms",
		protected(http.HandlerFunc(d.Room.List)))
	mux.Handle("POST /api/v1/rooms",
		protected(http.HandlerFunc(d.Room.Create)))
	mux.Handle("POST /api/v1/rooms/{id}/join",
		protected(http.HandlerFunc(d.Room.Join)))
	mux.Handle("POST /api/v1/rooms/{id}/leave",
		protected(http.HandlerFunc(d.Room.Leave)))
	mux.Handle("GET /api/v1/rooms/{id}/participants",
		protected(http.HandlerFunc(d.Room.Participants)))
	mux.Handle("PATCH /api/v1/rooms/{id}/participants",
		protected(http.HandlerFunc(d.Room.SetRole)))
	mux.Handle("POST /api/v1/rooms/{id}/raise-hand",
		protected(http.HandlerFunc(d.Room.RequestSpeak)))
	mux.Handle("GET /api/v1/rooms/{id}/speak-requests",
		protected(http.HandlerFunc(d.Room.SpeakRequests)))
	mux.Handle("POST /api/v1/rooms/{id}/invite",
		protected(http.HandlerFunc(d.Room.Invite)))
	mux.Handle("PATCH /api/v1/rooms/{id}/mute",
		protected(http.HandlerFunc(d.Room.SetMuted)))

	// --- media ---
	mux.Handle("POST /api/v1/media/upload-url",
		protected(http.HandlerFunc(d.Media.PrepareUpload)))
	mux.Handle("POST /api/v1/media/{id}/confirm",
		protected(http.HandlerFunc(d.Media.Confirm)))

	// --- WebSocket ---
	// Memakai middleware auth yang sama, hanya dengan izin tambahan membaca
	// token dari query string karena handshake WebSocket di browser tidak
	// bisa menyetel header Authorization.
	wsAuth := middleware.Auth(d.Verifier, middleware.AuthOptions{
		AllowQueryToken:  true,
		AllowDebugHeader: d.AllowDebugHeader,
	})
	mux.Handle("GET "+d.WSPath, wsAuth(d.WSHandler))

	// Rute tak dikenal tetap dibalas dalam bentuk JSON yang sama seperti
	// error lain, supaya klien tidak perlu menangani halaman 404 teks polos.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, http.StatusNotFound, httpx.CodeNotFound, "endpoint tidak ditemukan")
	})

	return middleware.Chain(mux,
		middleware.RequestID(),
		middleware.Recoverer(d.Log),
		middleware.CaptureErrors(),
		middleware.Logger(d.Log),
		middleware.CORS(d.CORSOrigins),
	)
}
