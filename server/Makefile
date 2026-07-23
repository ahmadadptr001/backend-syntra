.DEFAULT_GOAL := help

BINARY  := bin/syntra
PKG     := ./...
MAIN    := ./cmd/syntra

.PHONY: help
help: ## Tampilkan daftar perintah
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Rapikan dan unduh dependensi
	go mod tidy

.PHONY: run
run: ## Jalankan server secara lokal
	go run $(MAIN)

.PHONY: build
build: ## Kompilasi binary ke bin/
	go build -trimpath -ldflags="-s -w" -o $(BINARY) $(MAIN)

.PHONY: test
test: ## Jalankan test dengan race detector
	go test -race -count=1 $(PKG)

.PHONY: cover
cover: ## Jalankan test dan tampilkan ringkasan coverage
	go test -race -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -n 1

.PHONY: vet
vet: ## Analisis statis bawaan Go
	go vet $(PKG)

.PHONY: fmt
fmt: ## Format seluruh kode
	go fmt $(PKG)

.PHONY: check
check: fmt vet test ## Format, vet, lalu test

.PHONY: migrate
migrate: ## Petunjuk menerapkan migrasi ke Supabase
	@echo "Server ini tidak memegang kredensial database, jadi migrasi tidak"
	@echo "bisa dijalankan dari sini. Pilih salah satu:"
	@echo ""
	@echo "  1. Dashboard Supabase > SQL Editor — tempel isi tiap berkas, berurutan"
	@echo "  2. supabase db push  (butuh Supabase CLI + 'supabase link')"
	@echo ""
	@echo "Urutan berkas:"
	@ls -1 migrations/*.sql

.PHONY: up
up: ## Jalankan Redis + server lewat docker compose
	docker compose -f deployments/docker-compose.yml up --build

.PHONY: up-cluster
up-cluster: ## Jalankan dua instance server untuk menguji fanout antar-instance
	docker compose -f deployments/docker-compose.yml --profile cluster up --build

.PHONY: down
down: ## Hentikan docker compose
	docker compose -f deployments/docker-compose.yml down -v
