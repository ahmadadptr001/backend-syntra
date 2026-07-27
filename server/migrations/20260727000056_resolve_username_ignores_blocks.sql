-- Memperbaiki regresi dari 20260727000051_find_user_respects_blocks.sql.
--
-- MASALAH YANG SAYA BUAT SENDIRI
-- Migrasi 51 membuat find_user() mengembalikan nol baris bila ada blokir di antara dua
-- pengguna. Itu benar untuk PENCARIAN. Tetapi ProfileService.Block/Unblock juga memakai
-- jalur yang sama untuk menukar username menjadi id (repository FindID → find_user).
--
-- Akibatnya: begitu seseorang diblokir, ia tidak bisa lagi ditemukan — termasuk oleh
-- permintaan MEMBUKA blokirnya sendiri. FindID gagal, handler mengembalikan 404, dan
-- aplikasi menampilkan "Gagal membuka blokir. Periksa koneksi lalu coba lagi."
-- Koneksinya tidak pernah bermasalah; blokir itulah yang mengunci pintu keluarnya.
--
-- PERBAIKAN
-- Pemisahan yang seharusnya ada sejak awal: satu fungsi untuk MENEMUKAN orang (menghormati
-- blokir) dan satu fungsi untuk MENERJEMAHKAN username menjadi id (tidak menghormati
-- blokir, karena memang harus tetap bekerja justru saat blokir sedang aktif).
--
-- resolve_username tidak membocorkan apa pun yang belum diketahui pemanggil: ia hanya
-- memetakan username yang sudah dipegang pemanggil menjadi id, dan tidak mengembalikan
-- profil, foto, maupun status apa pun.

CREATE OR REPLACE FUNCTION public.resolve_username(p_username text)
RETURNS TABLE (id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT u.id
    FROM users u
    WHERE lower(u.username) = lower(btrim(p_username))
      AND u.deleted_at IS NULL
    LIMIT 1;
$$;

REVOKE ALL ON FUNCTION public.resolve_username(text) FROM public;
GRANT EXECUTE ON FUNCTION public.resolve_username(text) TO authenticated, service_role;
