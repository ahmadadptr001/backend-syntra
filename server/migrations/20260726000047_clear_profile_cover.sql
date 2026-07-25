-- Hapus background/cover profil.
--
-- update_my_profile memakai cover_media_id = COALESCE(p_cover_media, ...), jadi
-- mengirim NULL berarti "jangan ubah" -- tak ada cara mengosongkannya. Fungsi
-- khusus ini men-set cover_media_id = NULL untuk pemanggil. App memanggilnya lalu
-- menghapus berkas cover lama dari storage sendiri (id-nya sudah ia simpan lokal).
-- ASCII murni.

BEGIN;

CREATE OR REPLACE FUNCTION public.clear_profile_cover()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    UPDATE user_profiles
    SET cover_media_id = NULL, updated_at = now()
    WHERE user_id = v_user;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.clear_profile_cover() FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.clear_profile_cover() TO authenticated';
END;
$$;

COMMIT;
