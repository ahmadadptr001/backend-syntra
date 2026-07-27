-- Membatalkan blokir antara dua akun, ke DUA arah.
--
-- Diminta untuk: ahmadadptr@gmail.com  dan  citra@syntra.app
--
-- Dijalankan sebagai SQL langsung (bukan lewat RPC unblock_user) karena unblock_user
-- memakai require_auth() — ia hanya bisa menghapus blokir milik pengguna yang sedang
-- login. Di sini kita bertindak sebagai admin atas kedua akun sekaligus.
--
-- Jalankan di Supabase SQL editor. Aman diulang.

-- 1) Lihat dulu apa yang ada. Jalankan bagian ini sendiri untuk memastikan
--    kedua email benar-benar ketemu sebelum menghapus apa pun.
SELECT
    b.blocker_id,
    ub.email  AS blocker_email,
    ub.username AS blocker_username,
    b.blocked_id,
    tb.email  AS blocked_email,
    tb.username AS blocked_username,
    b.created_at
FROM blocks b
JOIN users ub ON ub.id = b.blocker_id
JOIN users tb ON tb.id = b.blocked_id
WHERE ub.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app')
   OR tb.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app');

-- 2) Hapus blokir di antara kedua akun tersebut, dua arah sekaligus.
DELETE FROM blocks b
USING users ub, users tb
WHERE b.blocker_id = ub.id
  AND b.blocked_id = tb.id
  AND ub.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app')
  AND tb.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app');

-- 3) Pastikan sudah bersih — harus mengembalikan 0 baris.
SELECT count(*) AS sisa_blokir
FROM blocks b
JOIN users ub ON ub.id = b.blocker_id
JOIN users tb ON tb.id = b.blocked_id
WHERE ub.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app')
  AND tb.email IN ('ahmadadptr@gmail.com', 'citra@syntra.app');
