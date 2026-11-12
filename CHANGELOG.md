# Changelog

Format: satu bagian per tag rilis. Bagian **Perlu tindakan** berisi hal yang
harus dilakukan operator saat upgrade (restart, ubah config, migrasi).
Release notes di GitHub diambil otomatis dari bagian tag yang bersangkutan
(`.github/workflows/release.yml`).

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
