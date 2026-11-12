# Changelog

Format: satu bagian per tag rilis. Bagian **Perlu tindakan** berisi hal yang
harus dilakukan operator saat upgrade (restart, ubah config, migrasi).
Release notes di GitHub diambil otomatis dari bagian tag yang bersangkutan
(`.github/workflows/release.yml`).

## [v0.4.0] - belum dirilis

Hasil uji end-to-end pertama dengan kiosk nyata (`pw-offstreet-cashless-v3`
v3.38.0b2) di PC lokal, 2026-11-12: jalur SDK → agent → dashboard terbukti,
yang dibenahi adalah kualitas sinyal dan keterbacaan.

### Agent
- Event `LOG_*` (catch-all dari logging handler kiosk) level WARNING
  diturunkan ke LOW: tidak mewarnai gate. ERROR tetap HIGH.
- Field `stack` pada event (traceback tanpa nilai variabel, maks 16 KiB),
  tersimpan dan tampil di detail event.
- `status_history`: perubahan ONLINE/STALE/DOWN dicatat tiap 30 detik;
  `status_since` di `/v1/fleet`, `status_history` di `/v1/devices/{id}`.
- Heartbeat membawa `platform` (`tongbao`/`windows`/`linux`).
- Registry: `PAYMENT_REJECTED` dan `INSUFFICIENT_BALANCE` → LOW; event baru
  `APP_EXCEPTION` (APP-003, HIGH), `QRIS_NOT_CONFIGURED` (PAY-004, LOW),
  `LPR_NO_FEED` (LPR-002, LOW), `MQTT_DISABLED` (MQTT-003, INFO),
  `GATE_OPEN_FAILED` (DEVICE-002, HIGH), `PCLESS_DISCONNECTED` (DEVICE-003, HIGH).
- Event yang pernah dipelajari agent dan kini bentrok dengan registry.yaml
  baru dipindah ke nomor berikutnya saat start (dicatat di log), sehingga
  satu kode tidak pernah dipakai dua nama.

### Dashboard
- Detail event dibaca sebagai label/nilai (Pesan, Terjadi di kiosk, Diterima
  agent, Device, Versi, Endpoint…), konteks, dan stack trace.
- Tombol **"Salin untuk debugging"**: ringkasan Markdown (gate, waktu, versi,
  pesan, konteks, stack, link timeline) untuk ditempel ke chat/issue/AI.
- Kartu gate: platform, **"Mati sejak HH.MM · lama"** saat STALE/DOWN.
- Halaman gate: status "sejak", Platform, tabel **Riwayat status**.

### SDK Python (0.3.0)
- `client.exception(exc=None, event=None, ...)`: kirim exception + traceback
  (frame saja, maks 30, tanpa variabel lokal). `error(..., stack=...)`.
- `platform=` di konstruktor; default dari OS, Tongbao mengirim `"tongbao"`.
- Pesan `SENTINEL_GAP` kini menghitung event yang tertahan dengan benar
  (sebelumnya selalu "0 event(s) waiting").

### Perlu tindakan
- Restart agent setelah upgrade; skema DB dimigrasi otomatis (kolom
  `events.stack`, `device_state.platform`, tabel `status_history`).
- Kiosk: vendor ulang SDK 0.3 dan ikuti `docs/integrasi-kiosk.md` §4b.

## [v0.3.0] - 2026-11-12

Rilis pertama yang diberi tag. Isinya seluruh pekerjaan MVP + hardening
sampai commit `3dbf3c3`, ditambah CI. Tidak ada perubahan perilaku agent
dibanding build `sentinel-agent-3dbf3c3-*`.

### Agent
- Heartbeat dan status gate ONLINE / STALE / DOWN / NEVER, endpoint
  `/v1/fleet`, batch ingest `/v1/events/batch`.
- Dashboard NOC tertanam (tema Nocturne, header nav, kartu event di HP),
  login `users.yaml` (bcrypt) + cookie HMAC; token device hanya untuk menulis.
- Alert Telegram: gate mati/pulih, event HIGH, grace period, cooldown,
  rute per severity, `thread_id` untuk forum topic, `retry_after` pada 429,
  pacing per chat, antrean pesan tersimpan di DB (tahan restart).
- Skala: idempotency dan fleet tetap memakai index pada jutaan event
  (`TestQueryPlansStayIndexed`), retensi dan purge berbatch, WAL.
- Normalisasi severity, kategori tak dikenal → APP dengan `category_hint`,
  alokasi kode stabil lintas restart, nomor ≥900 dicadangkan.
- Dead-man's switch `monitor.ping_url` / `SENTINEL_PING_URL`.
- CLI: `device add|list`, `user add|list`, `alert test`, `version`.

### SDK Python (0.2.0)
- Outbox memori/SQLite, backoff, batch, coalescing, `beat()` heartbeat
  watchdog, `SENTINEL_GAP` (SYS-901) saat outbox terputus.
- `on_send_error` kini benar-benar dipanggil (termasuk 401).
- Mendukung Python 3.6 sampai 3.12 (kiosk ARM memakai 3.6.9).

### Proses
- GitHub Actions: vet + race test, Python 3.6/3.10/3.12, cross-compile
  linux/amd64 · linux/armv7 · windows/amd64, E2E Playwright, pemeriksaan
  tidak ada file config/DB yang ter-commit.
- Workflow rilis: tag `v*` → build semua target + `SHA256SUMS` → GitHub Release.

### Perlu tindakan
- Tidak ada. Deploy pertama ke VPS ikuti `docs/deploy-vps.md`.
- Mulai rilis ini, deploy hanya dari artefak GitHub Release, bukan dari
  `dist/` lokal.
