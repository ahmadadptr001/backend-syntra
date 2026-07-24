package supabase

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const pathStorage = "/storage/v1"

// SignedUpload adalah izin unggah sekali pakai ke Supabase Storage.
type SignedUpload struct {
	// URL lengkap yang dituju klien. Klien mengunggah byte-nya langsung ke
	// sini — tidak melewati server Go sama sekali. Itulah inti pendekatan ini:
	// proses Go tidak boleh jadi perantara byte video, karena satu unggahan
	// 50 MB akan menahan memori dan goroutine tanpa memberi nilai apa pun.
	URL string `json:"url"`

	// Token yang sama sudah tertanam di URL; disediakan terpisah untuk klien
	// yang memakainya sebagai header.
	Token string `json:"token"`
}

type signedUploadResponse struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// CreateSignedUploadURL meminta izin unggah untuk sebuah path di bucket.
//
// Path yang sudah terpakai akan ditolak Supabase, jadi pemanggil harus
// memastikan path-nya unik — di proyek ini dijamin oleh UUID media.
func (c *Client) CreateSignedUploadURL(ctx context.Context, bucket, path string, opts ...Option) (SignedUpload, error) {
	if bucket == "" || path == "" {
		return SignedUpload{}, fmt.Errorf("supabase: bucket dan path wajib diisi")
	}

	endpoint := fmt.Sprintf("%s/object/upload/sign/%s/%s", pathStorage, bucket, strings.TrimPrefix(path, "/"))

	var resp signedUploadResponse
	if err := c.do(ctx, http.MethodPost, endpoint, map[string]any{}, &resp, opts...); err != nil {
		return SignedUpload{}, err
	}

	// Supabase mengembalikan URL relatif terhadap /storage/v1, misalnya
	// "/object/upload/sign/media/abc?token=xyz". Klien butuh yang absolut.
	full := resp.URL
	if strings.HasPrefix(full, "/") {
		full = c.baseURL + pathStorage + full
	}

	return SignedUpload{URL: full, Token: resp.Token}, nil
}

// DeleteObject menghapus satu objek dari bucket.
//
// Dipakai setelah baris metadata media di database hilang, untuk membuang
// berkasnya dari object storage. Memakai JWT pengguna (lewat Option), jadi
// tunduk pada RLS storage.objects — hanya pemilik berkas yang bisa
// menghapusnya, sejalan dengan penjaga di fungsi delete_media.
func (c *Client) DeleteObject(ctx context.Context, bucket, path string, opts ...Option) error {
	if bucket == "" || path == "" {
		return fmt.Errorf("supabase: bucket dan path wajib diisi")
	}

	endpoint := fmt.Sprintf("%s/object/%s/%s", pathStorage, bucket, strings.TrimPrefix(path, "/"))
	return c.do(ctx, http.MethodDelete, endpoint, nil, nil, opts...)
}

// PublicObjectURL menyusun URL baca untuk objek di bucket publik.
//
// Untuk bucket privat, URL ini akan ditolak — yang dibutuhkan adalah signed
// download URL, dan itu belum diimplementasikan karena bucket media proyek ini
// dibuat publik untuk sementara.
func (c *Client) PublicObjectURL(bucket, path string) string {
	return fmt.Sprintf("%s%s/object/public/%s/%s", c.baseURL, pathStorage, bucket, strings.TrimPrefix(path, "/"))
}
