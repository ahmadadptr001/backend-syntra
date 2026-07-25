// Cloudflare Worker: syntra-media-proxy
//
// Menyajikan media Syntra dari edge-cache Cloudflare, dan hanya menembak Supabase
// Storage saat cache MISS. Ini memangkas "cached egress" Supabase menjadi kira-
// kira satu tarikan per objek (sisanya dilayani gratis oleh Cloudflare).
//
// Alur URL:
//   Backend mengirim  https://cdn.syntra.fun/media/<key>
//   Worker memetakan   -> https://<project>.supabase.co/storage/v1/object/public/media/<key>
//
// URL media Syntra IMMUTABLE (media id ada di dalam path, foto/video baru = id
// baru), jadi aman di-cache selamanya.
//
// Deploy: lihat README.md di folder ini. Route: cdn.syntra.fun/*

// Ganti kalau project ref Supabase berubah.
const SUPABASE_PUBLIC = "https://tqcfmueshhpuqjuqgafi.supabase.co/storage/v1/object/public";

// Hanya proxy bucket publik "media" — cegah worker dipakai memproksi path lain.
const ALLOW_PREFIX = "/media/";

export default {
  async fetch(request) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("method not allowed", { status: 405 });
    }

    const url = new URL(request.url);
    if (!url.pathname.startsWith(ALLOW_PREFIX)) {
      return new Response("not found", { status: 404 });
    }

    const origin = SUPABASE_PUBLIC + url.pathname;

    // Teruskan request asli (termasuk header Range untuk video) ke origin, tetapi
    // suruh Cloudflare men-cache objeknya di edge selama setahun.
    const resp = await fetch(new Request(origin, request), {
      cf: { cacheEverything: true, cacheTtl: 31536000 },
    });

    const out = new Response(resp.body, resp);
    out.headers.set("Cache-Control", "public, max-age=31536000, immutable");
    out.headers.set("Access-Control-Allow-Origin", "*");
    return out;
  },
};
