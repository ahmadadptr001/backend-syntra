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
	Notif   *handler.Notification
	Profile *handler.Profile
	Call    *handler.Call
	Reel    *handler.Reel
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
	mux.Handle("DELETE /api/v1/conversations/{id}/messages",
		protected(http.HandlerFunc(d.Chat.ClearConversation)))

	// info & manajemen grup
	mux.Handle("GET /api/v1/conversations/{id}",
		protected(http.HandlerFunc(d.Chat.GetConversation)))
	mux.Handle("PATCH /api/v1/conversations/{id}",
		protected(http.HandlerFunc(d.Chat.UpdateGroup)))
	mux.Handle("POST /api/v1/conversations/{id}/leave",
		protected(http.HandlerFunc(d.Chat.LeaveConversation)))
	mux.Handle("PUT /api/v1/conversations/{id}/mute",
		protected(http.HandlerFunc(d.Chat.Mute)))
	mux.Handle("GET /api/v1/conversations/{id}/members",
		protected(http.HandlerFunc(d.Chat.ListMembers)))
	mux.Handle("POST /api/v1/conversations/{id}/members",
		protected(http.HandlerFunc(d.Chat.AddMembers)))
	mux.Handle("DELETE /api/v1/conversations/{id}/members/{user_id}",
		protected(http.HandlerFunc(d.Chat.RemoveMember)))
	mux.Handle("PATCH /api/v1/conversations/{id}/members/{user_id}",
		protected(http.HandlerFunc(d.Chat.SetMemberRole)))
	mux.Handle("GET /api/v1/conversations/{id}/reactions",
		protected(http.HandlerFunc(d.Chat.Reactions)))

	// reaksi pesan
	mux.Handle("PUT /api/v1/messages/{id}/reaction",
		protected(http.HandlerFunc(d.Chat.React)))
	mux.Handle("DELETE /api/v1/messages/{id}",
		protected(http.HandlerFunc(d.Chat.DeleteMessage)))
	// Bentuk bersarang yang dipakai aplikasi — setara dengan yang di atas.
	mux.Handle("DELETE /api/v1/conversations/{id}/messages/{message_id}",
		protected(http.HandlerFunc(d.Chat.DeleteMessageNested)))

	// --- story ---
	mux.Handle("GET /api/v1/stories",
		protected(http.HandlerFunc(d.Story.List)))
	mux.Handle("POST /api/v1/stories",
		protected(http.HandlerFunc(d.Story.Create)))
	mux.Handle("GET /api/v1/stories/me",
		protected(http.HandlerFunc(d.Story.ListMine)))
	mux.Handle("POST /api/v1/stories/{id}/view",
		protected(http.HandlerFunc(d.Story.MarkViewed)))
	mux.Handle("GET /api/v1/stories/{id}/viewers",
		protected(http.HandlerFunc(d.Story.Viewers)))
	mux.Handle("DELETE /api/v1/stories/{id}",
		protected(http.HandlerFunc(d.Story.Delete)))

	// --- direktori pengguna & graf pertemanan ---
	//
	// Pola "me/following" didaftarkan lebih dulu daripada "{username}".
	// ServeMux Go 1.22 memilih pola yang lebih spesifik, jadi urutan penulisan
	// sebenarnya tidak menentukan — tapi menulisnya begini membuat maksudnya
	// terbaca oleh manusia.
	mux.Handle("GET /api/v1/users/me",
		protected(http.HandlerFunc(d.Profile.GetMe)))
	mux.Handle("PATCH /api/v1/users/me",
		protected(http.HandlerFunc(d.Profile.UpdateMe)))
	mux.Handle("GET /api/v1/users/me/blocked",
		protected(http.HandlerFunc(d.Profile.ListBlocked)))
	mux.Handle("GET /api/v1/users/me/following",
		protected(http.HandlerFunc(d.User.ListFollowing)))
	mux.Handle("GET /api/v1/users/me/follow-requests",
		protected(http.HandlerFunc(d.User.FollowRequests)))
	mux.Handle("POST /api/v1/users/{username}/follow/approve",
		protected(http.HandlerFunc(d.User.ApproveFollow)))
	mux.Handle("POST /api/v1/users/{username}/follow/reject",
		protected(http.HandlerFunc(d.User.RejectFollow)))
	mux.Handle("GET /api/v1/users/{username}",
		protected(http.HandlerFunc(d.User.GetByUsername)))
	mux.Handle("POST /api/v1/users/{username}/follow",
		protected(http.HandlerFunc(d.User.Follow)))
	mux.Handle("DELETE /api/v1/users/{username}/follow",
		protected(http.HandlerFunc(d.User.Unfollow)))
	mux.Handle("POST /api/v1/users/{username}/block",
		protected(http.HandlerFunc(d.Profile.Block)))
	mux.Handle("DELETE /api/v1/users/{username}/block",
		protected(http.HandlerFunc(d.Profile.Unblock)))

	// --- perangkat (push notification) ---
	mux.Handle("POST /api/v1/devices",
		protected(http.HandlerFunc(d.Profile.RegisterDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}",
		protected(http.HandlerFunc(d.Profile.RevokeDevice)))

	// --- laporan (trust & safety) ---
	mux.Handle("POST /api/v1/reports",
		protected(http.HandlerFunc(d.Profile.Report)))

	// --- voice room ---
	mux.Handle("GET /api/v1/rooms",
		protected(http.HandlerFunc(d.Room.List)))
	mux.Handle("POST /api/v1/rooms",
		protected(http.HandlerFunc(d.Room.Create)))
	mux.Handle("POST /api/v1/rooms/{id}/join",
		protected(http.HandlerFunc(d.Room.Join)))
	mux.Handle("POST /api/v1/rooms/{id}/leave",
		protected(http.HandlerFunc(d.Room.Leave)))
	mux.Handle("POST /api/v1/rooms/{id}/end",
		protected(http.HandlerFunc(d.Room.End)))
	// Bentuk RESTful yang dipakai aplikasi — setara dengan POST .../end.
	mux.Handle("DELETE /api/v1/rooms/{id}",
		protected(http.HandlerFunc(d.Room.End)))
	mux.Handle("GET /api/v1/rooms/{id}/requests",
		protected(http.HandlerFunc(d.Room.JoinRequests)))
	mux.Handle("POST /api/v1/rooms/{id}/requests/{user_id}/approve",
		protected(http.HandlerFunc(d.Room.ApproveJoin)))
	mux.Handle("POST /api/v1/rooms/{id}/requests/{user_id}/reject",
		protected(http.HandlerFunc(d.Room.RejectJoin)))
	mux.Handle("GET /api/v1/rooms/{id}/participants",
		protected(http.HandlerFunc(d.Room.Participants)))
	mux.Handle("PATCH /api/v1/rooms/{id}/participants",
		protected(http.HandlerFunc(d.Room.SetRole)))
	mux.Handle("POST /api/v1/rooms/{id}/raise-hand",
		protected(http.HandlerFunc(d.Room.RequestSpeak)))
	mux.Handle("DELETE /api/v1/rooms/{id}/raise-hand",
		protected(http.HandlerFunc(d.Room.CancelSpeakRequest)))
	mux.Handle("GET /api/v1/rooms/{id}/speak-requests",
		protected(http.HandlerFunc(d.Room.SpeakRequests)))
	mux.Handle("POST /api/v1/rooms/{id}/invite",
		protected(http.HandlerFunc(d.Room.Invite)))
	mux.Handle("PATCH /api/v1/rooms/{id}/mute",
		protected(http.HandlerFunc(d.Room.SetMuted)))

	// --- telepon & video call ---
	//
	// Panggilan terikat pada percakapan. start memakai LiveKit yang sama dengan
	// voice room; tanpa LiveKit, sesi tetap tercatat tapi tidak keluar
	// suara/video (sfu_token kosong).
	mux.Handle("POST /api/v1/calls",
		protected(http.HandlerFunc(d.Call.Start)))
	mux.Handle("POST /api/v1/calls/{id}/answer",
		protected(http.HandlerFunc(d.Call.Answer)))
	mux.Handle("POST /api/v1/calls/{id}/decline",
		protected(http.HandlerFunc(d.Call.Decline)))
	mux.Handle("POST /api/v1/calls/{id}/leave",
		protected(http.HandlerFunc(d.Call.Leave)))
	mux.Handle("GET /api/v1/conversations/{id}/call",
		protected(http.HandlerFunc(d.Call.GetActive)))

	// --- reels / shorts ---
	//
	// Pola spesifik (me, saved, comments) didaftarkan bersama pola {id};
	// ServeMux Go 1.22 memilih yang paling spesifik, jadi urutan tak menentukan.
	mux.Handle("GET /api/v1/reels",
		protected(http.HandlerFunc(d.Reel.Feed)))
	mux.Handle("POST /api/v1/reels",
		protected(http.HandlerFunc(d.Reel.Create)))
	mux.Handle("GET /api/v1/reels/me",
		protected(http.HandlerFunc(d.Reel.ListMine)))
	mux.Handle("GET /api/v1/reels/saved",
		protected(http.HandlerFunc(d.Reel.ListSaved)))
	mux.Handle("GET /api/v1/reels/{id}",
		protected(http.HandlerFunc(d.Reel.Get)))
	mux.Handle("DELETE /api/v1/reels/{id}",
		protected(http.HandlerFunc(d.Reel.Delete)))
	mux.Handle("PUT /api/v1/reels/{id}/like",
		protected(http.HandlerFunc(d.Reel.Like)))
	mux.Handle("DELETE /api/v1/reels/{id}/like",
		protected(http.HandlerFunc(d.Reel.Unlike)))
	mux.Handle("PUT /api/v1/reels/{id}/save",
		protected(http.HandlerFunc(d.Reel.Save)))
	mux.Handle("DELETE /api/v1/reels/{id}/save",
		protected(http.HandlerFunc(d.Reel.Unsave)))
	mux.Handle("POST /api/v1/reels/{id}/view",
		protected(http.HandlerFunc(d.Reel.RecordView)))
	mux.Handle("GET /api/v1/reels/{id}/comments",
		protected(http.HandlerFunc(d.Reel.ListComments)))
	mux.Handle("POST /api/v1/reels/{id}/comments",
		protected(http.HandlerFunc(d.Reel.AddComment)))
	mux.Handle("DELETE /api/v1/reels/{id}/comments/{comment_id}",
		protected(http.HandlerFunc(d.Reel.DeleteComment)))
	mux.Handle("GET /api/v1/users/{username}/reels",
		protected(http.HandlerFunc(d.Reel.ListByUser)))

	// --- notifikasi ---
	//
	// unread-count dipisah dari daftarnya karena badge dipanggil jauh lebih
	// sering dan jawabannya jauh lebih kecil.
	mux.Handle("GET /api/v1/notifications",
		protected(http.HandlerFunc(d.Notif.List)))
	mux.Handle("GET /api/v1/notifications/unread-count",
		protected(http.HandlerFunc(d.Notif.UnreadCount)))
	mux.Handle("POST /api/v1/notifications/read",
		protected(http.HandlerFunc(d.Notif.MarkRead)))

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
