-- Siapa saja yang menonton sebuah story.
--
-- Tabel story_views sudah menyimpan datanya sejak awal, dan policy RLS-nya pun
-- sudah benar (pemilik story boleh melihat semua penontonnya; penonton hanya
-- boleh melihat catatannya sendiri). Yang belum ada: fungsi untuk membacanya,
-- sehingga daftar penonton tidak bisa ditampilkan sama sekali.
--
-- Hanya PEMILIK story yang boleh melihat daftarnya. Membukanya ke penonton lain
-- berarti memberi tahu siapa saja yang menyimak seseorang — informasi yang
-- tidak pernah mereka setujui untuk dibagikan.

BEGIN;

-- Index untuk urutan "penonton terbaru dulu". Tanpa ini, setiap pembacaan
-- harus memindai seluruh baris story_views milik story tersebut.
CREATE INDEX IF NOT EXISTS story_views_recent_idx
    ON story_views (story_id, viewed_at DESC, viewer_id DESC);

CREATE OR REPLACE FUNCTION public.list_story_viewers(
    p_story     uuid,
    p_before_at timestamptz DEFAULT NULL,
    p_before_id uuid        DEFAULT NULL,
    p_limit     integer     DEFAULT 50
)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    viewed_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_owner uuid;
BEGIN
    SELECT s.author_id INTO v_owner
    FROM stories s
    WHERE s.id = p_story AND s.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'story tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_owner <> v_user THEN
        RAISE EXCEPTION 'hanya pemilik story yang boleh melihat penontonnya'
            USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT sv.viewer_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           sv.viewed_at
    FROM story_views sv
    JOIN users u ON u.id = sv.viewer_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = sv.viewer_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE sv.story_id = p_story
      -- Cursor gabungan (waktu, id). Memakai waktu saja tidak cukup: dua orang
      -- bisa menonton pada milidetik yang sama, dan salah satunya akan
      -- terlewat saat berpindah halaman.
      AND (
            p_before_at IS NULL
         OR (sv.viewed_at, sv.viewer_id) < (p_before_at, COALESCE(p_before_id, '00000000-0000-0000-0000-000000000000'::uuid))
          )
    ORDER BY sv.viewed_at DESC, sv.viewer_id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 200);
END;
$$;

-- Ringkasan penonton beberapa story sekaligus.
--
-- Layar arsip menampilkan banyak story sekaligus; menanyakan jumlah penonton
-- satu per satu berarti satu permintaan per story. Fungsi ini menjawab
-- semuanya dalam satu perjalanan.
CREATE OR REPLACE FUNCTION public.story_view_counts(p_stories uuid[])
RETURNS TABLE (story_id uuid, view_count integer)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    RETURN QUERY
    SELECT s.id, s.view_count
    FROM stories s
    WHERE s.id = ANY(COALESCE(p_stories, ARRAY[]::uuid[]))
      AND s.author_id = v_user      -- hanya story sendiri
      AND s.deleted_at IS NULL;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.list_story_viewers(uuid, timestamptz, uuid, integer) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.list_story_viewers(uuid, timestamptz, uuid, integer) TO authenticated;

REVOKE EXECUTE ON FUNCTION public.story_view_counts(uuid[]) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.story_view_counts(uuid[]) TO authenticated;

-- Menyelaraskan ulang view_count dengan jumlah baris sebenarnya.
--
-- view_count adalah kolom denormalisasi yang dinaikkan mark_story_viewed. Kalau
-- ada baris story_views yang terhapus di luar alur itu, angkanya melenceng.
-- Sekali jalan saat migrasi; kalau nanti sering melenceng, jadikan job berkala.
UPDATE stories s
SET view_count = COALESCE(c.n, 0)
FROM (
    SELECT sv.story_id, count(*)::integer AS n
    FROM story_views sv
    GROUP BY sv.story_id
) c
WHERE c.story_id = s.id AND s.view_count <> c.n;

COMMIT;
