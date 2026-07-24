// Package app merakit seluruh komponen menjadi satu aplikasi.
//
// Di sinilah — dan hanya di sini — dependency injection dilakukan secara
// manual. Tidak ada framework DI: sekali baca fungsi New, seluruh grafik
// dependensi aplikasi terlihat utuh, dan urutan startup maupun shutdown
// menjadi eksplisit.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ahmadadptr001/backend-syntra/internal/auth"
	"github.com/ahmadadptr001/backend-syntra/internal/config"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/account"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/call"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/chat"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/media"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/notification"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/presence"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/reel"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/room"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/story"
	"github.com/ahmadadptr001/backend-syntra/internal/domain/user"
	"github.com/ahmadadptr001/backend-syntra/internal/pkg/id"
	"github.com/ahmadadptr001/backend-syntra/internal/platform/cache"
	"github.com/ahmadadptr001/backend-syntra/internal/platform/livekit"
	"github.com/ahmadadptr001/backend-syntra/internal/platform/pubsub"
	"github.com/ahmadadptr001/backend-syntra/internal/platform/supabase"
	"github.com/ahmadadptr001/backend-syntra/internal/repository/redisstore"
	repo "github.com/ahmadadptr001/backend-syntra/internal/repository/supabase"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/handler"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/rest/middleware"
	"github.com/ahmadadptr001/backend-syntra/internal/transport/ws"
)

// App memegang seluruh komponen yang berumur sepanjang proses.
type App struct {
	cfg    *config.Config
	log    *slog.Logger
	supa   *supabase.Client
	redis  *goredis.Client
	hub    *ws.Hub
	rooms  *room.Service
	server *http.Server
}

// New membangun aplikasi. Kegagalan di sini berarti proses tidak boleh jalan.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	// --- platform ---
	supa, err := supabase.New(supabase.Options{
		URL:            cfg.Supabase.URL,
		AnonKey:        cfg.Supabase.AnonKey,
		ServiceRoleKey: cfg.Supabase.ServiceRoleKey,
		Timeout:        cfg.Supabase.Timeout,
	}, log)
	if err != nil {
		return nil, err
	}

	// Kegagalan menghubungi Supabase saat startup tidak menghentikan proses.
	// Berbeda dengan koneksi database yang butuh handshake, ini hanya HTTP —
	// gangguan sesaat pada jaringan tidak seharusnya membuat pod gagal boot
	// dan masuk crash loop. Kesiapan sesungguhnya dilaporkan /readyz.
	if err := supa.Ping(ctx); err != nil {
		log.Warn("supabase belum bisa dihubungi saat startup", "error", err)
	}

	rdb, err := cache.NewRedis(ctx, cfg.Redis.URL)
	if err != nil {
		return nil, err
	}

	// --- realtime ---
	instanceID := id.New()
	bridge := pubsub.NewRedis(rdb, instanceID, log)
	hub := ws.NewHub(bridge, log)

	// --- domain ---
	chatRepo := repo.NewChatRepository(supa)
	storyRepo := repo.NewStoryRepository(supa)
	userRepo := repo.NewUserRepository(supa)
	mediaRepo := repo.NewMediaRepository(supa)
	mediaStorage := repo.NewMediaStorage(supa)
	presenceStore := redisstore.NewPresence(rdb)
	presenceVisibility := repo.NewPresenceVisibilityRepository(supa)

	accountRepo := repo.NewAccountRepository(supa)
	notifRepo := repo.NewNotificationRepository(supa)
	profileRepo := repo.NewProfileRepository(supa)
	roomRepo := repo.NewRoomRepository(supa)
	callRepo := repo.NewCallRepository(supa)
	reelRepo := repo.NewReelRepository(supa)
	sfu := livekit.New(cfg.LiveKit.APIKey, cfg.LiveKit.APISecret, cfg.LiveKit.URL)
	if !sfu.Configured() {
		log.Warn("LiveKit belum dikonfigurasi: voice room bisa dibuat tapi TIDAK akan mengeluarkan suara",
			"petunjuk", "isi LIVEKIT_API_KEY, LIVEKIT_API_SECRET, dan LIVEKIT_URL di .env")
	}

	mediaService := media.NewService(mediaRepo, mediaStorage, cfg.Supabase.StorageBucket)
	chatService := chat.NewService(chatRepo, ws.NewPublisher(hub), log, mediaService.PublicURL)
	storyService := story.NewService(storyRepo, ws.NewPublisher(hub))
	userService := user.NewService(userRepo)
	presenceService := presence.NewService(presenceStore, cfg.WS.PresenceTTL, presenceVisibility)
	roomService := room.NewService(roomRepo, sfu, ws.NewPublisher(hub))
	// sfu memenuhi TokenIssuer sekaligus WebhookVerifier — objek yang sama
	// menerbitkan token dan memverifikasi webhook LiveKit.
	callService := call.NewService(callRepo, sfu, sfu, ws.NewPublisher(hub))
	reelService := reel.NewService(reelRepo, ws.NewPublisher(hub))
	notifService := notification.NewService(notifRepo, ws.NewPublisher(hub))
	profileService := account.NewProfileService(profileRepo, profileRepo, ws.NewPublisher(hub), mediaService.PublicURL)

	// --- transport: websocket ---
	wsRouter := ws.NewRouter(log)
	ws.RegisterHandlers(wsRouter, chatService, chatRepo, presenceService)

	wsHandler := ws.NewHandler(hub, wsRouter, presenceService, ws.Options{
		ReadLimitBytes: cfg.WS.ReadLimitBytes,
		WriteWait:      cfg.WS.WriteWait,
		PongWait:       cfg.WS.PongWait,
		PingInterval:   cfg.WS.PingInterval,
		SendBuffer:     cfg.WS.SendBuffer,
		MaxTopics:      200,
	}, cfg.WS.AllowedOrigins, log)

	// --- transport: rest ---
	verifier := newVerifier(cfg, supa, log)

	// Rate limit per pengguna (melengkapi batas per-IP nginx). Dimatikan kalau
	// RATE_LIMIT_PER_MINUTE <= 0 — limiter dibiarkan nil dan rutenya tidak
	// dibungkus sama sekali.
	var limiter middleware.Limiter
	if rl := redisstore.NewRateLimiter(rdb, cfg.RateLimit.PerMinute, time.Minute, log); !rl.Disabled() {
		limiter = rl
		log.Info("rate limit per pengguna aktif", "per_minute", cfg.RateLimit.PerMinute)
	} else {
		log.Info("rate limit per pengguna dimatikan (RATE_LIMIT_PER_MINUTE <= 0)")
	}

	router := rest.NewRouter(rest.Deps{
		Log:              log,
		Verifier:         verifier,
		CORSOrigins:      cfg.HTTP.CORSOrigins,
		AllowDebugHeader: cfg.Auth.DevBypass && !cfg.IsProduction(),
		WSPath:           cfg.WS.Path,
		WSHandler:        wsHandler,
		Limiter:          limiter,
		Health: handler.NewHealth(cfg.Version, map[string]handler.Check{
			"supabase": supa.Ping,
			"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		}),
		Account: handler.NewAccount(
			account.NewService(accountRepo, accountRepo),
		),
		Chat:    handler.NewChat(chatService, mediaService),
		Story:   handler.NewStory(storyService, mediaService),
		User:    handler.NewUser(userService, mediaService),
		Media:   handler.NewMedia(mediaService),
		Room:    handler.NewRoom(roomService, mediaService),
		Notif:   handler.NewNotification(notifService, mediaService),
		Profile: handler.NewProfile(profileService, mediaService),
		Call:    handler.NewCall(callService),
		Reel:    handler.NewReel(reelService, mediaService),
	})

	server := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: router,

		// ReadHeaderTimeout melindungi dari Slowloris tanpa mengganggu
		// koneksi WebSocket.
		//
		// ReadTimeout dan WriteTimeout sengaja TIDAK diisi: keduanya memasang
		// deadline absolut pada koneksi, dan pada socket berumur panjang itu
		// berarti pemutusan berkala yang terlihat seperti gangguan jaringan
		// acak. Batas waktu per operasi sudah ditangani sendiri oleh client.go
		// lewat SetReadDeadline/SetWriteDeadline.
		ReadHeaderTimeout: cfg.HTTP.ReadTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,

		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	return &App{
		cfg:    cfg,
		log:    log,
		supa:   supa,
		redis:  rdb,
		hub:    hub,
		rooms:  roomService,
		server: server,
	}, nil
}

// closeStaleRooms menutup room yang ditinggalkan tanpa sempat diakhiri.
//
// Host yang aplikasinya tertutup paksa, kehabisan baterai, atau kehilangan
// jaringan tidak pernah memanggil leave. Tanpa pembersihan berkala, room itu
// menetap sebagai "live" selamanya dan menumpuk di daftar sebagai room hantu —
// yang memang sudah terjadi sebelum ini ada.
func (a *App) closeStaleRooms(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			closed, err := a.rooms.CloseStale(ctx, 30)
			if err != nil {
				a.log.Warn("gagal menutup room terbengkalai", "error", err)
				continue
			}
			if closed > 0 {
				a.log.Info("room terbengkalai ditutup", "jumlah", closed)
			}
		}
	}
}

// Run menjalankan aplikasi sampai ctx dibatalkan atau ada komponen yang gagal.
func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		a.log.Info("server berjalan",
			"addr", a.cfg.HTTP.Addr,
			"ws_path", a.cfg.WS.Path,
			"env", a.cfg.Env,
			"version", a.cfg.Version,
			"supabase", a.cfg.Supabase.URL,
			"env_file", a.cfg.EnvFile,
		)
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := a.hub.Run(ctx); err != nil {
			errCh <- fmt.Errorf("ws bridge: %w", err)
		}
	}()

	go a.closeStaleRooms(ctx)

	select {
	case <-ctx.Done():
		a.log.Info("sinyal berhenti diterima, memulai graceful shutdown")
		a.shutdown()
		return nil

	case err := <-errCh:
		a.shutdown()
		return err
	}
}

func (a *App) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.HTTP.ShutdownTimeout)
	defer cancel()

	// Socket ditutup lebih dulu, dan urutannya bukan kebetulan: setiap
	// koneksi WebSocket berjalan di dalam sebuah handler HTTP, sehingga
	// server.Shutdown akan menunggu selamanya selama masih ada socket
	// terbuka. Menutupnya duluan juga membuat klien menerima frame close
	// yang benar dan langsung reconnect ke instance lain.
	a.hub.CloseAll()

	if err := a.server.Shutdown(ctx); err != nil {
		a.log.Warn("graceful shutdown melewati batas waktu, menutup paksa", "error", err)
		_ = a.server.Close()
	}

	if err := a.redis.Close(); err != nil {
		a.log.Warn("gagal menutup koneksi redis", "error", err)
	}
}

// newVerifier memilih strategi autentikasi sesuai environment.
func newVerifier(cfg *config.Config, supa *supabase.Client, log *slog.Logger) auth.Verifier {
	if cfg.Auth.DevBypass && !cfg.IsProduction() {
		log.Warn("AUTENTIKASI DILUMPUHKAN: AUTH_DEV_BYPASS aktif, header X-Debug-User diperlakukan sebagai identitas pengguna")
		log.Warn("token palsu bukan JWT Supabase, jadi auth.uid() kosong dan seluruh query akan ditolak RLS")
		return auth.DevVerifier{}
	}

	if supa.HasServiceRole() {
		log.Warn("SUPABASE_SERVICE_ROLE_KEY terpasang: kunci ini melewati RLS, pastikan hanya dipakai untuk pekerjaan latar")
	}

	return auth.NewSupabaseVerifier(
		supa.BaseURL(),
		supa.AnonKey(),
		supa.HTTPClient(),
		cfg.Supabase.AuthCacheTTL,
	)
}
