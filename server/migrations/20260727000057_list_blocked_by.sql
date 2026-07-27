-- Siapa yang memblokir SAYA.
--
-- Dibutuhkan supaya sisi yang diblokir bisa menampilkan keadaan yang benar sejak
-- aplikasi dibuka, bukan hanya ketika kebetulan menerima siaran user.blocked. Tanpa ini,
-- cold start selalu memulai dengan anggapan "tidak ada yang memblokir saya", lalu
-- menampilkan nama, foto, dan status aktif orang yang jelas-jelas sudah memblokir.
--
-- SOAL PRIVASI. Fungsi ini memang memberi tahu pemanggil bahwa ia diblokir. Itu memang
-- yang diminta produk: pemblokiran yang tidak terlihat oleh pihak yang diblokir tidak
-- ada gunanya — pesannya ditolak server, tetapi tampilannya berpura-pura normal.
-- Yang dikembalikan dibatasi seminimal mungkin: id dan username saja. Tidak ada nama
-- tampilan, foto, waktu pemblokiran, maupun alasan.

CREATE OR REPLACE FUNCTION public.list_blocked_by()
RETURNS TABLE (
    user_id  uuid,
    username text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT u.id, u.username
    FROM blocks b
    JOIN users u ON u.id = b.blocker_id
    WHERE b.blocked_id = public.require_auth()
      AND u.deleted_at IS NULL;
$$;

REVOKE ALL ON FUNCTION public.list_blocked_by() FROM public;
GRANT EXECUTE ON FUNCTION public.list_blocked_by() TO authenticated, service_role;
