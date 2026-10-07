# Rencana: AI-assisted debugging (belum dikembangkan)

> Status: **rencana saja.** Dikerjakan setelah dashboard stabil, rollout STI,
> dan canary selesai dievaluasi. Belum ada kode untuk fitur ini.

## Tujuan

Mempercepat siklus *error di gate → penyebab ditemukan → perbaikan siap diuji*,
tanpa pernah mengubah aplikasi produksi secara otomatis.

```
error di gate ─► Sentinel (VPS) ─► developer bertanya ke AI di repo lokal
                                    │  AI membaca event + stack trace + source code
                                    ▼
                                usulan patch di branch lokal ─► test ─► review manusia ─► build ─► deploy manual
```

## Prinsip

1. **Deteksi, usulan patch, dan deploy dipisah.** AI boleh membaca data Sentinel
   dan source code, lalu menulis patch **di branch lokal developer**. Build dan
   deploy ke kiosk tetap keputusan manusia.
2. **Tidak ada data sensitif.** Stack trace hanya berisi nama file, fungsi, dan
   nomor baris, **tanpa nilai variabel**. Nomor kartu, saldo, kredensial, dan
   isi request/response tidak pernah dikirim ke Sentinel.
3. **Akses AI ke Sentinel hanya baca**, memakai user dashboard ber-role `viewer`.
   Token kiosk tidak dipakai, karena token itu hanya untuk menulis event.

## Penyesuaian dengan arsitektur sekarang

- Sentinel Agent berjalan di **VPS pusat**, bukan di kiosk. Kiosk hanya memakai SDK.
- "Developer Agent" **tidak perlu dibangun dari nol**. Claude Code di PC
  developer sudah bisa membaca repo kiosk dan membuat patch. Yang kurang dari
  sisi Sentinel adalah (a) lokasi kode di setiap error, dan (b) cara AI membaca
  data Sentinel.

## Tingkat kemampuan

| Tingkat | Isi | Status |
|---|---|---|
| 1. Monitoring | deteksi, dashboard, alert | **sudah ada** (MVP) |
| 2. AI coding assistant | AI menganalisis error dan mengusulkan patch di repo lokal | rencana ini |
| 3. Automated repair | patch → test → deploy otomatis | **tidak direncanakan**; kalau nanti diinginkan, wajib lewat PR + CI + persetujuan manusia + canary per gate |

## Pekerjaan (tingkat 2)

1. **Stack trace di SDK.** `client.exception(exc, event=...)`, ditambah tangkapan
   otomatis di `_SentinelLoggingHandler` saat `exc_info` ada. Field `stack`
   bersifat opsional di protokol (kompatibel mundur, `docs/protocol-v1.md`).
   Path dibuat relatif terhadap root repo, dan nilai variabel lokal tidak dikirim.
2. **Pengelompokan error.** Agent menghitung *fingerprint* (event + frame teratas)
   sehingga terlihat "bug yang sama terjadi di 5 gate", lengkap dengan versi
   aplikasi tempat bug pertama dan terakhir muncul.
3. **MCP server read-only** (`sentinel-agent mcp`, login dengan user viewer).
   Tool yang disediakan: `list_problem_gates`, `get_device`, `search_events`,
   `get_error_group`. Developer cukup bertanya di repo kiosk, misalnya
   "analisis error READER di STI-G03 tadi pagi".
4. **Tombol "Salin untuk debugging"** di dashboard. Isinya ringkasan Markdown:
   event, kode error, versi aplikasi, stack, dan timeline ±10 menit di sekitar kejadian.
   Bisa ditempel ke AI apa pun tanpa perlu MCP.

## Prasyarat sebelum mulai

- Rollout STI selesai, dan canary memberi gambaran error nyata yang paling sering terjadi.
- Repo kiosk sudah memakai SDK 0.2 (outbox, heartbeat, `api_error`).
- Keputusan soal model/AI yang dipakai dan kebijakan data (data apa saja yang
  boleh dibaca AI) disetujui pemilik sistem.

## Ukuran keberhasilan

- Waktu dari error pertama sampai patch siap diuji turun (diukur saat canary vs setelah fitur ini).
- Tidak ada satu pun data sensitif di field `stack` atau `context` (diaudit lewat sampel).
