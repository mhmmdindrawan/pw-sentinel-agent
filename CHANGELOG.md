# Changelog

Format: satu bagian per tag rilis. Bagian **Perlu tindakan** berisi hal yang
harus dilakukan operator saat upgrade (restart, ubah config, migrasi).
Release notes di GitHub diambil otomatis dari bagian tag yang bersangkutan
(`.github/workflows/release.yml`).

## [v0.6.0] - belum dirilis

Hasil memantau kiosk nyata 24 jam di PC uji: 91 % event adalah `LOG_*`
(satu server yang tidak terjangkau = ±970 baris/hari, gate merah terus).
Rilis ini membuat agent tetap tenang dan ringan saat kiosk berisik, dan
mengganti "satu pesan Telegram per event" dengan "satu pesan per insiden".

### Agent
- **Pagar catch-all**: event `LOG_*` level ERROR/CRITICAL kini maksimal
  MEDIUM (kuning), WARNING tetap LOW. Log aplikasi tidak pernah membuat
  gate merah; yang merah hanya event terstruktur.
- **Squelch** (`ingest.squelch_per_hour`, default 20): event bernama sama
  dari satu kiosk setelah 20× dalam satu jam tidak disimpan satu per satu,
  melainkan diringkas menjadi satu event "Berulang N× lagi…" di akhir jam
  (`context.squelched_count`). Kiosk tetap mendapat 200 sehingga outbox
  SDK kosong. Kerja agent per event tetap konstan; memori dibatasi jumlah
  pasangan kiosk+event per jam.
- **Insiden** (`incident.gap_minutes`, default 10): event MEDIUM+ yang
  beruntun pada satu gate, dan status DOWN, dikelompokkan menjadi satu
  insiden (tabel `incidents`: mulai, terakhir, jumlah, komposisi event,
  severity terburuk). Selesai otomatis setelah gate online dan tenang
  selama `gap_minutes`. `/v1/fleet` membawa `incidents` (aktif),
  `/v1/devices/{id}` membawa 20 insiden terakhir.
- **Telegram per insiden**: satu pesan saat insiden dibuka
  ("🔴 PCC G01 Gate Utara — INSIDEN #12 · mulai 14:05 · 3 event · Pembaca
  kartu tidak siap ×2 …"), **diedit** (bukan dikirim ulang) paling cepat
  tiap 5 menit saat bertambah, dan diedit terakhir kali menjadi
  "🟢 … SELESAI · 35 mnt · 12 event". Pesan MATI/online terpisah tidak
  dikirim lagi; event UNMAPPED dan antrean outbox tetap seperti semula.
  Nomor pesan disimpan di DB sehingga restart agent melanjutkan edit pesan
  yang sama. `incident.disabled: true` mengembalikan perilaku lama.

### Dashboard
- Panel **Insiden aktif** di atas daftar gate (gate, nama, sejak, lama,
  jumlah, komposisi), tabel **Insiden** di halaman gate.
- Peringatan **event paling berisik** di halaman gate bila satu nama event
  ≥100× dan ≥50 % dari semua event 24 jam, dengan saran untuk developer.
- Nama manusia untuk event, kode pendek (`APP-010`), kolom "Komponen";
  tombol "Salin untuk debugging" bekerja di HTTP biasa.

### Dokumen
- `docs/prompt-integrasi-gateout.md`: tahap 2 (server tidak terjangkau =
  satu event transisi), bagian per platform (Linux / Windows / Tongbao).
- `docs/deploy-server-lokasi.md`: rute Telegram per lokasi, catatan
  dashboard pusat.

### Penghapusan harian
- `retention.daily_reset_at: "02:00"` (jam server; `retention.timezone` opsional): tiap hari
  pada jam itu event, insiden, dan riwayat status sebelum jam itu dihapus
  lalu DB dipadatkan (VACUUM). Kiosk terdaftar, state device, user, dan kode
  error tetap. Agent yang mati saat jam reset mengejar saat start. Kosong =
  mati (berlaku `days`).

### Keputusan
- Data hanya untuk hari operasional berjalan (reset 02:00).
- Telegram **tidak dipakai** di rollout (rawan; dashboard Sentinel sudah
  cukup). Alerter tetap ada tetapi nonaktif selama token/rute kosong.

### Perlu tindakan
- Restart agent; tabel `incidents` dibuat otomatis. Tidak ada perubahan
  config wajib (nilai default aktif). Kiosk tidak perlu diubah.

## [v0.5.0] - belum dirilis

Model deployment baru: **satu agent per lokasi** di server lokal (PCC 2
gate, PJT 4 gate, …), kiosk mendaftar sendiri dengan kunci lokasi, dan nama
gate diatur dari aplikasi gateout. Agent pusat di VPS dengan `devices.yaml`
tetap didukung.

### Agent
- `site:` di config.yaml (`code`, `name`, `enroll_key` / env
  `SENTINEL_SITE_KEY`). `POST /v1/enroll`: kiosk mengirim kunci lokasi +
  `device_id`/`gate`/`pos`/`gate_name`/`platform` dan menerima token
  miliknya sendiri (hanya hash SHA-256 yang disimpan, tabel `devices`).
  Enroll ulang merotasi token; entri `devices.yaml` tidak bisa diambil alih
  (409); kunci salah dibatasi per IP (429). `location` selalu `site.code`.
- Heartbeat membawa `gate_name`, `gate`, `pos`: nama gate tampil di kartu,
  device hasil enroll bisa pindah gate dari pengaturan aplikasi tanpa
  restart agent.
- `/v1/fleet` menambah `site` dan `agent` (versi, alamat, IP Tailscale,
  ukuran DB, batas, sisa disk); tiap device membawa `name` dan `enrolled`.
- `retention.max_db_mb` (default 500): di atas batas ini event tertua
  dibuang lebih awal dari `days`, dicatat di log. `devices.yaml` opsional
  bila enrollment aktif.
- CLI: `device list` menampilkan device enroll, `device revoke -id`,
  `backup -out FILE` (`VACUUM INTO`, tanpa sqlite3).

### Dashboard
- Baris lokasi di bawah judul: `PCC · Plaza Central City · agent
  100.x.y.z:8787 · vX · pendaftaran kiosk aktif`; judul tab memakai kode lokasi.
- Nama gate di kartu dan judul halaman gate; pencarian juga mencocokkan
  nama dan alamat. Halaman gate menyebut asal token (enroll / devices.yaml).
- Peringatan kuning saat DB >80 % batas atau sisa disk <1 GB.

### SDK Python (0.3.0)
- `enroll(base_url, site_key, device_id, gate, pos=, gate_name=,
  platform=, app_version=)` → dict dengan `token`; `EnrollError(status)`.
- `gate=`, `gate_name=`, `pos=` di konstruktor, ikut setiap heartbeat.

### Dokumen
- `docs/deploy-server-lokasi.md` (runbook per lokasi, beban, backup),
  `docs/prompt-integrasi-gateout.md` (prompt untuk repo aplikasi gateout),
  protocol-v1 §Enrollment, integrasi-kiosk §2 alur enroll.

### Perlu tindakan
- Restart agent; migrasi otomatis (kolom `device_state.name`, tabel
  `devices`). Tanpa `site:` perilaku sama seperti 0.4.
- Server lokasi baru: isi `site.code/name`, set `SENTINEL_SITE_KEY`, lalu
  kiosk diisi `SENTINEL_SITE_KEY` + `SENTINEL_GATE(_NAME)` (SDK 0.3).

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
