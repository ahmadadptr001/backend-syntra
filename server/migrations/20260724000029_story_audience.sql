-- Audiens sebuah story — untuk siaran realtime story.new (pesan-untuk-backend 11.5).
--
-- Saat seseorang memposting story, backend perlu tahu KE SIAPA menyiarkan
-- event-nya. Himpunannya harus sama persis dengan siapa yang nanti melihat
-- story itu di GET /stories (fungsi list_stories), kalau tidak ada perangkat
-- yang dapat notifikasi untuk story yang tak akan pernah ia lihat, atau
-- sebaliknya.
--
-- list_stories menampilkan story dari: penulisnya sendiri, orang yang di-follow
-- dengan status 'accepted', DAN lawan bicara di percakapan yang sama — dikurangi
-- blokir dua arah. Fungsi ini membalik sudut pandangnya: diberi seorang penulis,
-- siapa saja yang berhak melihat story-nya. (Visibility per-story sengaja tidak
-- disaring di sini, mengikuti perilaku list_stories yang sekarang.)
--
-- Dipakai hanya oleh penulisnya sendiri: p_author harus sama dengan pemanggil,
-- supaya audiens orang lain tidak bisa dienumerasi.

BEGIN;

CREATE OR REPLACE FUNCTION public.story_audience(p_author uuid)
RETURNS TABLE (user_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_author <> v_user THEN
        RAISE EXCEPTION 'hanya boleh untuk diri sendiri' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT DISTINCT aud.uid
    FROM (
        -- pengikut yang sudah diterima
        SELECT f.follower_id AS uid
        FROM follows f
        WHERE f.followee_id = p_author AND f.status = 'accepted'

        UNION

        -- lawan bicara di percakapan yang sama
        SELECT them.user_id AS uid
        FROM conversation_members me
        JOIN conversation_members them
          ON them.conversation_id = me.conversation_id
        WHERE me.user_id = p_author AND me.left_at IS NULL
          AND them.user_id <> p_author AND them.left_at IS NULL
    ) aud
    WHERE aud.uid <> p_author
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = aud.uid AND b.blocked_id = p_author)
               OR (b.blocker_id = p_author AND b.blocked_id = aud.uid)
      );
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.story_audience(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.story_audience(uuid) TO authenticated';
END;
$$;

COMMIT;
