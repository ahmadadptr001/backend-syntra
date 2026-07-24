-- Menghapus satu media milik pengguna beserta berkasnya — atas permintaan.
--
-- Konteksnya dari pesan-untuk-backend.md poin 5: saat pengguna mengganti foto
-- profil, foto lama tertinggal di object storage selamanya. Aplikasi sudah
-- menyimpan media_id lama dan memanggil deleteMedia(oldId) tiap ganti avatar —
-- sampai sekarang itu no-op karena endpoint-nya belum ada.
--
-- Ini BERBEDA dari purge_orphan_media(): purge bekerja di latar setelah masa
-- tenggang untuk media yatim; fungsi ini penghapusan langsung yang diminta
-- pemiliknya. Karena langsung, ia butuh dua penjaga yang purge tidak perlu:
--
--   * hanya pemilik yang boleh menghapus medianya (bukan sembarang orang);
--   * media yang MASIH ditunjuk sesuatu tidak boleh dihapus. Ini bukan sekadar
--     kerapian: sebagian foreign key ke media_assets memakai ON DELETE CASCADE
--     (stories, message_attachments), sehingga menghapus barisnya diam-diam
--     ikut menghapus story atau lampiran pesan yang masih memakainya; yang lain
--     ON DELETE SET NULL (avatar/cover profil, avatar percakapan), yang akan
--     mengosongkan avatar seseorang tanpa ia sadari; dan reels memakai NO
--     ACTION, yang menolak dengan galat foreign key mentah. Semua kasus itu
--     ditolak lebih dulu di sini dengan pesan yang jelas.
--
-- Aplikasi memang baru memanggil ini SETELAH profil menunjuk avatar baru, jadi
-- media lama sudah yatim saat sampai ke sini. Kalau ternyata belum, menolak
-- jauh lebih baik daripada merusak tautan di tempat lain.
--
-- storage_key dikembalikan supaya backend tahu berkas mana yang harus dibuang
-- dari object storage setelah barisnya hilang.

BEGIN;

CREATE OR REPLACE FUNCTION public.delete_media(p_id uuid)
RETURNS TABLE (storage_key text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_owner uuid;
    v_key   text;
BEGIN
    SELECT ma.owner_id, ma.storage_key INTO v_owner, v_key
    FROM media_assets ma
    WHERE ma.id = p_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'media tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_owner <> v_user THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    -- Referensi diperiksa apa adanya, tanpa menyaring baris yang sudah
    -- di-soft-delete: foreign key ditegakkan pada barisnya, bukan pada status
    -- tampilnya. Kalau story sudah disembunyikan tetapi barisnya masih
    -- menunjuk media ini, penghapusan tetap akan menabrak FK — jadi lebih baik
    -- menolaknya di sini dengan pesan yang bisa dibaca.
    IF EXISTS (SELECT 1 FROM stories s              WHERE s.media_id = p_id)
    OR EXISTS (SELECT 1 FROM message_attachments a  WHERE a.media_id = p_id)
    OR EXISTS (SELECT 1 FROM reels r                WHERE r.media_id = p_id)
    OR EXISTS (SELECT 1 FROM user_profiles p        WHERE p.avatar_media_id = p_id OR p.cover_media_id = p_id)
    OR EXISTS (SELECT 1 FROM conversations c        WHERE c.avatar_media_id = p_id)
    OR EXISTS (SELECT 1 FROM rooms rm               WHERE rm.recording_media_id = p_id)
    THEN
        RAISE EXCEPTION 'media masih dipakai' USING ERRCODE = '55006';
    END IF;

    DELETE FROM media_assets ma WHERE ma.id = p_id;

    RETURN QUERY SELECT v_key;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.delete_media(uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.delete_media(uuid) TO authenticated;

COMMIT;
