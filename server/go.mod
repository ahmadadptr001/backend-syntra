module github.com/ahmadadptr001/backend-syntra

go 1.23.0

// Hanya dua dependensi pihak ketiga.
//
// Driver PostgreSQL tidak ada di sini karena aplikasi tidak terhubung langsung
// ke database: akses data lewat API HTTP Supabase, dan untuk itu net/http dari
// stdlib sudah cukup.
require (
	github.com/gorilla/websocket v1.5.3
	github.com/redis/go-redis/v9 v9.7.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
)
