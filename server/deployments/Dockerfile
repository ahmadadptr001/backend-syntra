# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.23-alpine AS build

WORKDIR /src

# Dependensi disalin lebih dulu supaya layer cache-nya tidak ikut batal
# setiap kali ada perubahan kode.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO dimatikan agar binary benar-benar statis dan bisa berjalan di image
# scratch tanpa libc.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/syntra ./cmd/syntra

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

# Sertifikat root dibutuhkan untuk TLS ke Supabase; image distroless static
# sudah membawanya.
COPY --from=build /out/syntra /syntra

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/syntra"]
