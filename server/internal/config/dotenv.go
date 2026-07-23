package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// maxParentLookup membatasi seberapa jauh ke atas berkas .env dicari.
//
// Berguna ketika binary dijalankan dari subfolder — misalnya `go run
// ./cmd/syntra` yang dieksekusi dari dalam cmd/, atau binary hasil build yang
// diletakkan di bin/.
const maxParentLookup = 3

// loadDotEnv memuat pasangan KEY=VALUE dari berkas .env ke environment proses.
//
// Ditulis sendiri alih-alih memakai library karena kebutuhannya sekitar empat
// puluh baris, dan satu dependensi lagi tidak sepadan untuk itu.
//
// Aturan yang dipegang:
//
//   - Environment variable yang SUDAH ada tidak pernah ditimpa. Ini penting:
//     di server produksi nilainya datang dari orchestrator, dan sebuah berkas
//     .env yang tidak sengaja ikut ter-deploy tidak boleh membajaknya.
//   - Berkas yang tidak ada bukan error. Di produksi memang seharusnya tidak ada.
//
// Mengembalikan path berkas yang dipakai, atau string kosong kalau tidak ada.
func loadDotEnv(name string) string {
	path, ok := findEnvFile(name)
	if !ok {
		return ""
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	firstLine := true

	for scanner.Scan() {
		line := scanner.Text()

		// Notepad di Windows menyimpan berkas dengan BOM UTF-8. Tanpa
		// dibuang, kunci pertama terbaca sebagai BOM+APP_ENV dan diam-diam
		// tidak pernah cocok dengan apa pun.
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}

		key, value, ok := parseLine(line)
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}

	return path
}

func findEnvFile(name string) (string, bool) {
	// ENV_FILE menang atas pencarian otomatis.
	if custom := os.Getenv("ENV_FILE"); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom, true
		}
		return "", false
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}

	for i := 0; i <= maxParentLookup; i++ {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // sudah di akar drive
		}
		dir = parent
	}

	return "", false
}

func parseLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	line = strings.TrimPrefix(line, "export ")

	key, rawValue, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}

	return key, parseValue(strings.TrimSpace(rawValue)), true
}

func parseValue(raw string) string {
	// Nilai berkutip diambil apa adanya, termasuk tanda pagar di dalamnya.
	if len(raw) >= 2 {
		first, last := raw[0], raw[len(raw)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			inner := raw[1 : len(raw)-1]
			if first == '"' {
				inner = strings.ReplaceAll(inner, `\n`, "\n")
			}
			return inner
		}
	}

	// Untuk nilai tanpa kutip, komentar sebaris dipotong — tapi hanya kalau
	// tanda pagarnya didahului spasi. Tanpa syarat itu, sandi Redis yang
	// mengandung '#' akan terpotong diam-diam.
	for _, marker := range []string{" #", "\t#"} {
		if i := strings.Index(raw, marker); i >= 0 {
			raw = raw[:i]
		}
	}

	return strings.TrimSpace(raw)
}
