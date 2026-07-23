-- Buang overload send_message 6-argumen yang bikin pesan teks hilang.
--
-- Migrasi 14 menambah send_message versi 7-argumen (dengan p_media) TANPA
-- membuang versi 6-argumen dari migrasi 02. Akibatnya ada DUA fungsi
-- send_message. Saat backend memanggil dengan 6 argumen (pesan teks tanpa
-- lampiran), PostgREST menghadapi dua kandidat yang sama-sama cocok
-- (6-argumen persis, atau 7-argumen dengan p_media default). Pada kondisi ini
-- panggilan bisa membalas 2xx tetapi pesan TIDAK benar-benar tersimpan —
-- persis gejala yang muncul: kirim pesan balas 201, tapi hilang saat dibuka
-- ulang, dan reaksi ke pesan itu balas 404 "pesan tidak ditemukan".
--
-- Perbaikannya dua lapis:
--   1. Backend kini SELALU mengirim p_media (array kosong bila tak ada), jadi
--      hanya versi 7-argumen yang cocok.
--   2. Migrasi ini membuang versi 6-argumen sepenuhnya, menghapus ambiguitas
--      dari akar untuk semua pemanggil.

BEGIN;

DROP FUNCTION IF EXISTS public.send_message(uuid, uuid, text, text, uuid, timestamptz);

COMMIT;
