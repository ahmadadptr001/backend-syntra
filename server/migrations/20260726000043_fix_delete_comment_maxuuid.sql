-- Perbaiki delete_reel_comment: hapus komentar apa pun error "reel tidak ditemukan".
--
-- Migrasi 40 menambah cascade (hapus induk => balasan ikut). Tapi ia memakai
--   SELECT count(*), max(reel_id) INTO v_deleted, v_reel FROM removed;
-- dan reel_id bertipe uuid. PostgreSQL TIDAK punya agregat max(uuid) -> setiap
-- pemanggilan gagal dengan SQLSTATE 42883 ("function max(uuid) does not exist"),
-- yang oleh PostgREST dipetakan ke HTTP 404 -> klien menerima "reel tidak
-- ditemukan" untuk SEMUA penghapusan komentar (induk maupun balasan).
--
-- Perbaikan: reel_id sudah pasti sama untuk komentar target dan seluruh
-- balasannya (satu reel). Ambil lewat subquery skalar dari CTE target yang
-- berisi tepat satu baris (id komentar adalah primary key), jadi tak perlu
-- agregat sama sekali. count(*) untuk jumlah baris yang benar-benar terhapus
-- tetap dari CTE removed.
--
-- Perilaku & izin identik dengan migrasi 40; hanya cara mengambil reel_id yang
-- diperbaiki. CREATE OR REPLACE, signature & GRANT lama tetap. ASCII murni.

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
        RETURNING 1
    )
    SELECT count(*), (SELECT reel_id FROM target)
    INTO v_deleted, v_reel
    FROM removed;

    IF COALESCE(v_deleted, 0) = 0 THEN
        RAISE EXCEPTION 'komentar tidak ditemukan atau bukan milikmu' USING ERRCODE = '42501';
    END IF;

    UPDATE reels
    SET comment_count = GREATEST(comment_count - v_deleted, 0)
    WHERE id = v_reel;
END;
$$;

COMMIT;
