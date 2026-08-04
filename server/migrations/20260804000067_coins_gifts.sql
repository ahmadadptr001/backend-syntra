-- Koin & GIF gift untuk live.
--
-- Penonton mengirim GIF/gift ke host saat live; tiap gift berharga koin. Backend
-- menjadi OTORITAS saldo: pengurangan koin dilakukan atomik di dalam satu fungsi
-- (SECURITY DEFINER) supaya tidak bisa dicurangi dari klien. Pengiriman gift lalu
-- disiarkan ke kanal live:<id> lewat WebSocket (event live.gift) — tabelnya di sini
-- hanya mencatat saldo, katalog, dan riwayat; penyiaran realtime ada di Go.

BEGIN;

-- Dompet koin per pengguna.
CREATE TABLE IF NOT EXISTS coin_wallets (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    balance    integer NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Katalog GIF/gift. Server yang memegang harga — klien tak boleh menentukannya.
CREATE TABLE IF NOT EXISTS gifts (
    id     uuid PRIMARY KEY,
    code   text UNIQUE NOT NULL,
    emoji  text NOT NULL,
    name   text NOT NULL,
    cost   integer NOT NULL CHECK (cost > 0),
    active boolean NOT NULL DEFAULT true,
    sort   integer NOT NULL DEFAULT 0
);

-- Riwayat kirim gift di live.
CREATE TABLE IF NOT EXISTS live_gifts (
    id         uuid PRIMARY KEY,
    live_id    uuid NOT NULL REFERENCES lives(id) ON DELETE CASCADE,
    sender_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    gift_id    uuid NOT NULL REFERENCES gifts(id),
    coins      integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS live_gifts_live_idx ON live_gifts (live_id, created_at DESC);

ALTER TABLE coin_wallets ENABLE ROW LEVEL SECURITY;
ALTER TABLE gifts        ENABLE ROW LEVEL SECURITY;
ALTER TABLE live_gifts   ENABLE ROW LEVEL SECURITY;

-- Katalog awal — samakan dengan daftar di app (kode, emoji, nama, harga).
INSERT INTO gifts (id, code, emoji, name, cost, sort) VALUES
    (gen_random_uuid(), 'rose',    '🌹', 'Mawar',   1,   1),
    (gen_random_uuid(), 'heart',   '💖', 'Hati',    5,   2),
    (gen_random_uuid(), 'clap',    '👏', 'Tepuk',   8,   3),
    (gen_random_uuid(), 'fire',    '🔥', 'Api',     12,  4),
    (gen_random_uuid(), 'party',   '🎉', 'Pesta',   20,  5),
    (gen_random_uuid(), 'crown',   '👑', 'Mahkota', 50,  6),
    (gen_random_uuid(), 'unicorn', '🦄', 'Unicorn', 99,  7),
    (gen_random_uuid(), 'rocket',  '🚀', 'Roket',   199, 8),
    (gen_random_uuid(), 'diamond', '💎', 'Berlian', 500, 9)
ON CONFLICT (code) DO NOTHING;

-- ============================================================
-- FUNGSI
-- ============================================================

-- Saldo koin pemanggil; membuat dompet dengan bonus perkenalan bila belum ada.
CREATE OR REPLACE FUNCTION public.get_wallet()
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_balance integer;
BEGIN
    INSERT INTO coin_wallets (user_id, balance) VALUES (v_user, 120)
    ON CONFLICT (user_id) DO NOTHING;

    SELECT balance INTO v_balance FROM coin_wallets WHERE user_id = v_user;
    RETURN v_balance;
END;
$$;

-- Isi ulang koin (SEMENTARA tanpa pembayaran nyata — placeholder sampai ada gateway).
CREATE OR REPLACE FUNCTION public.topup_wallet(p_amount integer)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_balance integer;
BEGIN
    IF p_amount IS NULL OR p_amount <= 0 OR p_amount > 100000 THEN
        RAISE EXCEPTION 'jumlah isi ulang tidak valid' USING ERRCODE = '22023';
    END IF;

    INSERT INTO coin_wallets (user_id, balance) VALUES (v_user, 120)
    ON CONFLICT (user_id) DO NOTHING;

    UPDATE coin_wallets
    SET balance = balance + p_amount, updated_at = now()
    WHERE user_id = v_user
    RETURNING balance INTO v_balance;

    RETURN v_balance;
END;
$$;

-- Katalog gift yang aktif.
CREATE OR REPLACE FUNCTION public.list_gifts()
RETURNS TABLE (id uuid, code text, emoji text, name text, cost integer)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    PERFORM public.require_auth();
    RETURN QUERY
    SELECT g.id, g.code, g.emoji, g.name, g.cost
    FROM gifts g
    WHERE g.active
    ORDER BY g.sort, g.cost;
END;
$$;

-- Kirim gift ke sebuah live: kurangi koin ATOMIK, catat, kembalikan detail + saldo.
-- p_id = id baris live_gifts (dibuat di Go, seperti pola id lain).
CREATE OR REPLACE FUNCTION public.send_live_gift(p_id uuid, p_live uuid, p_gift uuid)
RETURNS TABLE (emoji text, name text, cost integer, balance integer, sender_username text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_cost     integer;
    v_emoji    text;
    v_name     text;
    v_balance  integer;
    v_username text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM lives WHERE id = p_live AND status = 'live') THEN
        RAISE EXCEPTION 'live tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    SELECT g.cost, g.emoji, g.name INTO v_cost, v_emoji, v_name
    FROM gifts g WHERE g.id = p_gift AND g.active;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'gift tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    INSERT INTO coin_wallets (user_id, balance) VALUES (v_user, 120)
    ON CONFLICT (user_id) DO NOTHING;

    -- Pengurangan atomik: hanya berhasil kalau saldo mencukupi. Tanpa baris yang
    -- ter-update berarti koin kurang.
    UPDATE coin_wallets
    SET balance = balance - v_cost, updated_at = now()
    WHERE user_id = v_user AND balance >= v_cost
    RETURNING balance INTO v_balance;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'koin tidak cukup' USING ERRCODE = 'P0003';
    END IF;

    INSERT INTO live_gifts (id, live_id, sender_id, gift_id, coins)
    VALUES (p_id, p_live, v_user, p_gift, v_cost);

    SELECT username INTO v_username FROM users WHERE id = v_user;

    RETURN QUERY SELECT v_emoji, v_name, v_cost, v_balance, v_username::text;
END;
$$;

-- ============================================================
-- HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_wallet()',
        'public.topup_wallet(integer)',
        'public.list_gifts()',
        'public.send_live_gift(uuid, uuid, uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
