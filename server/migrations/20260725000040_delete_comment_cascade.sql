-- Hapus komentar induk => balasannya (child) ikut terhapus.
--
-- delete_reel_comment dulu hanya menandai deleted_at pada satu komentar. Kalau
-- yang dihapus adalah komentar top-level yang punya balasan, balasannya tetap
-- ada dan menggantung tanpa induk. Versi ini mengikutkan seluruh child (balasan
-- langsung; kedalaman balasan memang dibatasi satu tingkat oleh add_reel_comment)
-- dan mengurangi comment_count sebanyak jumlah baris yang benar-benar terhapus.
--
-- Izin tetap sama: penulis komentar, atau pemilik reel. CREATE OR REPLACE, jadi
-- signature & GRANT lama tetap berlaku. Ditulis ASCII murni.

BEGIN;

CREATE OR REPLACE FUNCTION public.delete_reel_comment(p_comment uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_reel    uuid;
    v_deleted integer;
BEGIN
    -- Tandai komentar target + semua balasannya sebagai terhapus, sekaligus.
    -- Otorisasi dinilai pada komentar TARGET (p_comment): penulisnya, atau
    -- pemilik reel. Sekali lolos, child-nya ikut karena berada di reel yang sama.
    WITH target AS (
        SELECT c.id, c.reel_id
        FROM reel_comments c
        WHERE c.id = p_comment
          AND c.deleted_at IS NULL
          AND (
                c.author_id = v_user
             OR EXISTS (SELECT 1 FROM reels r WHERE r.id = c.reel_id AND r.author_id = v_user)
          )
    ),
    removed AS (
        UPDATE reel_comments c
        SET deleted_at = now()
        FROM target t
        WHERE c.deleted_at IS NULL
          AND (c.id = t.id OR c.parent_comment_id = t.id)
        RETURNING c.reel_id
    )
    SELECT count(*), max(reel_id) INTO v_deleted, v_reel FROM removed;

    IF COALESCE(v_deleted, 0) = 0 THEN
        RAISE EXCEPTION 'komentar tidak ditemukan atau bukan milikmu' USING ERRCODE = '42501';
    END IF;

    UPDATE reels
    SET comment_count = GREATEST(comment_count - v_deleted, 0)
    WHERE id = v_reel;
END;
$$;

COMMIT;
