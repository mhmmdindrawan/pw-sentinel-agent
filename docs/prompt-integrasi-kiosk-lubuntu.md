# Prompt: integrasi Parkways Sentinel ke kiosk Lubuntu (Tongbao)

Cara pakai:

1. Buka Claude Code **di repo aplikasi kiosk Lubuntu**.
2. Lampirkan tiga hal dari repo Sentinel ini:
   - folder `sdk/python/parkways_sentinel/` (4 file),
   - `docs/protocol-v1.md`,
   - `agent/registry.yaml` (daftar nama event dan kategorinya).
3. Salin semua isi blok `text` di bawah sebagai pesan pertama.

Prompt ini menggabungkan pelajaran dari integrasi aplikasi gateout
(`prompt-integrasi-gateout.md` dan `prompt-perbaikan-daftar-gateout.md`).
Kalau repo kiosk ternyata sudah punya `app/sentinel/`, langkah 0 di prompt
akan mendeteksinya dan hanya memperbaiki yang kurang.

---

```text
Kamu bekerja di repo aplikasi kiosk parkir yang berjalan di Lubuntu
(perangkat Tongbao: ARM, Python 3.6.9, hardware gate/reader/palang).
Aplikasi ini punya DUA mode koneksi ke hardware: MQTT dan TCP. Satu
kiosk memakai salah satunya, dipilih lewat config. Integrasi harus benar
dan stabil di kedua mode.
Tugasmu: hubungkan aplikasi ini ke Parkways Sentinel, yaitu sistem
pemantauan per lokasi. Satu PC server di setiap lokasi menjalankan agent
Sentinel, dan semua kiosk di lokasi itu melapor ke sana. Kiosk mengirim
event terstruktur dan heartbeat. Sentinel hanya mengamati: jangan
mengubah alur transaksi, pembayaran, MQTT, atau kontrol hardware.

Terlampir: SDK Python `parkways_sentinel/` (client.py, events.py,
outbox.py, __init__.py), `protocol-v1.md`, dan `registry.yaml`.

## Batasan keras

- Python 3.6.9 di ARM. Jangan pakai: f-string `=`, walrus, dataclasses,
  `from __future__ import annotations`, `subprocess.run(capture_output=)`,
  `asyncio.run`, typing 3.7+. f-string biasa boleh (3.6 mendukung). Hanya
  stdlib, tanpa dependensi baru.
- Sentinel tidak boleh memblokir atau membuat crash aplikasi. Semua
  jaringan Sentinel berjalan di thread latar. Kalau Sentinel gagal apa pun
  sebabnya, aplikasi tetap jalan normal (kill-switch `_NoOpClient`).
- Jangan pernah mengirim data pelanggan: nomor kartu, saldo, plat nomor
  lengkap, nama, kredensial, atau isi payload pembayaran. `context` hanya
  berisi kode, status, durasi, nama port, dan jumlah retry.
- Jangan pernah mencatat kunci lokasi atau token di log, layar, atau git.
- File config yang memuat token: tulis atomik (file sementara +
  `os.replace`), lalu chmod 0600.

## 0. Audit dulu, laporkan sebelum mengubah

Cari dan laporkan:
- Apakah sudah ada integrasi Sentinel (`sentinel`, `parkways_sentinel`,
  `SENTINEL_` di kode/config)? Kalau ada, versinya dan apa yang kurang
  dibanding daftar di bawah.
- Di mana config aplikasi disimpan, dan SEMUA tempat yang menulis file
  itu (form pengaturan, restore, updater, migrasi). Penulis yang
  menulis ulang seluruh file wajib mempertahankan kunci `SENTINEL_*`.
  Di aplikasi gateout, kunci-kunci ini pernah hilang karena form lain
  menyimpan isian kosong sebagai None lalu menghapus kuncinya.
- Main loop / siklus polling utama (untuk heartbeat).
- Tempat kegagalan hardware dan jaringan dideteksi: reader kartu,
  palang/PCLess, MQTT, printer, kamera LPR, server pembayaran.
- Mode koneksi MQTT vs TCP: kunci config yang memilihnya, modul yang
  aktif di tiap mode, dan perangkat apa yang lewat transport itu (palang,
  reader, LED, loop detector, dll.). Buat tabel: perangkat → transport →
  fungsi/callback tempat putus, gagal kirim, dan pulih terdeteksi.
- Library MQTT yang dipakai (mis. paho-mqtt) dan versinya, apakah
  memakai `loop_start()` (thread sendiri) atau `loop_forever()`, dan di
  mana `on_connect` / `on_disconnect` ditangani. Untuk TCP: socket
  langsung atau library, siapa yang melakukan reconnect, berapa timeout.
- Apa saja yang sekarang ditulis ke log oleh jalur MQTT/TCP: frekuensi
  per menit saat normal dan saat putus (perkirakan dari kode), dan
  apakah ada log berisi bytes mentah/hex dari hardware.
- Cara log aplikasi disimpan di disk (file, rotasi, ukuran) dan media
  penyimpanan perangkat (SD card/eMMC).
- Cara aplikasi dijalankan di Lubuntu (autostart, systemd, script), dan
  cara build/distribusinya (PyInstaller di ARM?).

## 1. Vendor SDK

Salin `parkways_sentinel/` ke dalam paket aplikasi, misalnya
`app/sentinel/parkways_sentinel/`. Buat `app/sentinel/client.py` dengan:
- `get_client()`: singleton lazy, thread-safe.
- `_NoOpClient`: punya `error()`, `warning()`, `info()`, `api_error()`,
  `exception()`, `beat()`, `flush()`, `close()`, semuanya tidak melakukan
  apa pun.
- `_SentinelLoggingHandler`: handler logging catch-all yang meneruskan
  WARNING+ sebagai event `LOG_<LOGGER>__<FUNC>`. Record dengan
  `extra={"sentinel": False}` dilewati. Handler tidak boleh meneruskan log
  dari modul Sentinel sendiri (cegah loop).
Kalau build memakai PyInstaller, pastikan `sqlite3` dan semua modul SDK
ikut ter-bundle (cek file .toc hasil build).

## 2. Kunci config

| Kunci | Isi | Diisi oleh |
|---|---|---|
| SENTINEL_AGENT_URL | `http://<IP server lokasi>:8787` | teknisi |
| SENTINEL_SITE_KEY | kunci lokasi (32 hex, dari `cat ~/sentinel-local/site.key` di server) | teknisi, sekali |
| SENTINEL_DEVICE_ID | unik per kiosk, mis. `BBD-G01-POS01` | teknisi |
| SENTINEL_GATE | kode gate, mis. `G01` | teknisi |
| SENTINEL_GATE_NAME | nama untuk tim support, mis. `Gate Keluar 1` | teknisi |
| SENTINEL_POS | opsional, mis. `POS01` | teknisi |
| SENTINEL_TOKEN | dari agent setelah daftar; jangan pernah ditampilkan | otomatis |
| SENTINEL_OUTBOX_PATH | default `~/.bssparking/sentinel_outbox.db` | otomatis |

## 3. Pendaftaran (enroll), kontrak agent

`POST {agent_url}/v1/enroll` tanpa Authorization, body JSON:
`site_key`, `device_id`, `gate` (wajib), `pos`, `gate_name`, `platform`,
`app_version` (opsional). Pakai `parkways_sentinel.enroll(...)` yang sudah
ada (timeout 5 s, melempar `EnrollError` yang membawa status HTTP).
- 201 → `{device_id, location, gate, pos, site_name, token}`. Token
  hanya dikirim sekali: simpan ke SENTINEL_TOKEN, lalu baca ulang file
  untuk memastikan tersimpan.
- 400 isian tidak valid, 401 kunci lokasi salah, 404 pendaftaran mati di
  agent, 409 device_id statis di agent, 429 terlalu banyak kunci salah.
  Body error selalu `{"error": "..."}`.
- `GET {agent_url}/v1/health` → `{"status":"ok","version":...}`, tanpa
  login. Dipakai untuk tombol "Tes koneksi".
- Daftar ulang dengan device_id yang sama merotasi token. Dua kiosk
  dengan ID sama akan saling merebut token, jadi ID harus unik.

Aturan:
- Daftar hanya kalau SENTINEL_TOKEN kosong dan SENTINEL_SITE_KEY terisi.
- Enroll di thread latar, jangan di thread UI atau thread transaksi.
- Gagal jaringan (timeout, connection refused, 5xx): ulang di latar
  dengan jeda 10 s, 30 s, 60 s, lalu tiap 5 menit, tanpa restart
  aplikasi. 400/401/404/409: berhenti dan tampilkan pesannya. 429:
  tunggu 5 menit.
- Tanpa token, jangan buat client yang mengirim tanpa token (agent
  produksi menolaknya dengan 401). Status menjadi
  "Belum terdaftar: isi Kunci lokasi".
- Kalau agent menolak token saat mengirim (401 di `on_send_error`):
  kosongkan SENTINEL_TOKEN dan jadwalkan daftar ulang memakai kunci
  lokasi yang masih tersimpan.

## 4. Layar Pengaturan Sentinel

Tambahkan di menu Pengaturan/Konfigurasi yang sudah ada (ikuti toolkit UI
aplikasi). Kolomnya sesuai tabel langkah 2 tanpa TOKEN dan OUTBOX.
Validasi sebelum simpan, dengan pesan di layar itu sendiri:
- Agent URL: wajib, diawali `http://` atau `https://`, ada host dan
  port. Buang spasi dan `/` di akhir.
- Kunci lokasi: wajib selama belum ada token. Buang spasi dan baris baru
  (sering disalin dari terminal). Tampilkan bertopeng (••••).
- Device ID: wajib, `^[A-Za-z0-9._-]{3,64}$`, TOLAK yang hanya angka
  (`1`, `01`). Isi usulan otomatis `<GATE>-<POS>` bila kosong.
- Gate: wajib, `^[A-Za-z0-9._-]{1,16}$`. Nama gate ≤ 64 karakter. POS
  opsional, pola sama dengan Device ID.
Tombol:
- "Tes koneksi": GET /v1/health, timeout 3 s → "Agent OK (v0.6.0)" atau
  sebab gagal. Untuk timeout tambahkan "cek Tailscale / jaringan, dan
  pastikan akun Tailscale kiosk sama dengan server".
- "Simpan & daftar": simpan, lalu enroll di latar. Status di layar
  "Mendaftar…" → "Terdaftar sebagai BBD G01 (BCT Baddoka)" atau pesan
  gagal per status.
- "Daftar ulang": kosongkan token, lalu daftar lagi.
UI tidak boleh beku: semua jaringan di thread latar, update UI lewat
mekanisme thread-safe toolkit (mis. Tkinter `after()`).

## 5. Client dan heartbeat

`SentinelClient(device_id, base_url, token, app_version, platform="tongbao",
gate, gate_name, pos, outbox_path, on_send_error)`.
- `on_send_error` mencatat ke log level DEBUG saja (WARNING akan
  ditangkap handler catch-all dan berputar menjadi event baru).
- Outbox SQLite di disk: event selama agent/jaringan putus disimpan lalu
  dikirim setelah tersambung. Pastikan folder outbox ada.
- `get_client().beat()` di setiap siklus main loop / polling utama, BUKAN
  dari timer atau thread terpisah. SDK sendiri membatasi kirim 1×/60 s.
  Kalau main loop hang, heartbeat berhenti dan dashboard menandai gate
  STALE lalu DOWN. Itulah tujuannya.
- Mengubah gate/nama gate di pengaturan: buat ulang client (close lalu
  buat baru) atau tambahkan `set_identity()` kecil yang thread-safe di
  SDK; tidak perlu restart aplikasi.
- Saat aplikasi keluar: `get_client().close(timeout=2)`.

## 6. Event terstruktur (pakai nama dari registry.yaml)

Kirim saat TRANSISI (sekali saat putus, sekali saat pulih), bukan setiap
retry atau setiap siklus. Buat helper kecil `Transition(name)` dengan
`down(event, **ctx)` / `up()` yang menyimpan state per komponen.
- APP_STARTED: info, sekali saat start (setelah guard single-instance).
- Reader kartu: READER_* (putus, gagal baca berulang, timeout).
- Palang/PCLess: PCLESS_DISCONNECTED, GATE_OPEN_FAILED.
- MQTT dan TCP: lihat langkah 6a.
- Server pembayaran: satu Transition untuk "server pembayaran",
  HTTP_SERVER_ERROR / NETWORK_DISCONNECTED saat pertama gagal,
  SERVER_RECOVERED (context {"down_s": n}) saat pulih; PAYMENT_TIMEOUT
  dan HTTP_TIMEOUT tetap untuk kejadian nyata. Log ERROR/WARNING di
  jalur retry diberi `extra={"sentinel": False}`. Target: satu gangguan
  server = paling banyak 3 event, bukan ratusan.
- Printer, kamera LPR (LPR_NO_FEED), CCTV: pola yang sama.
- Pulih (severity INFO, tidak pernah membuat kartu gate kuning/merah),
  context {"down_s": n}: READER_RECOVERED, LPR_RECOVERED,
  CCTV_RECOVERED, MQTT_RECOVERED, PCLESS_RECOVERED, SERVER_RECOVERED.
- Exception: di `sys.excepthook`, `threading` excepthook (3.6 tidak punya
  `threading.excepthook`: bungkus `run()` worker sendiri), dan try/except
  terluar controller → `get_client().exception()`. Jangan kirim
  traceback yang sama dua kali (lewat handler catch-all juga).
Semua nama event di prompt ini sudah terdaftar di registry.yaml (agent
v1.0.0+), jadi kodenya sama di semua lokasi. Pakai konstanta dari
`parkways_sentinel.events`. Kalau ada kondisi penting lain tanpa nama di
registry.yaml, daftarkan sebagai usulan di laporan akhir dan kirim dengan
`category` yang tepat. Jangan mengarang kode error sendiri: kode dibuat
oleh agent.

## 6a. Mode MQTT dan mode TCP

Agent sudah mengenal `platform="tongbao"` sebagai label "Tongbao" di
dashboard. Tetap kirim platform itu di kedua mode. Mode koneksi dikirim
lewat `context`, bukan lewat platform.
- Baca mode SEKALI saat start dari config yang sudah ada. Simpan sebagai
  `TRANSPORT = "mqtt"` atau `"tcp"`.
- APP_STARTED: context `{"transport": TRANSPORT, "hw_host": <host:port
  broker/controller tanpa kredensial>}`.
- Setiap event hardware/koneksi membawa `context["transport"]`.

Mode MQTT:
- Broker putus (dari `on_disconnect` dengan rc != 0, atau loop
  reconnect gagal): MQTT_DISCONNECTED, context {"transport":"mqtt",
  "rc": rc}. Sekali per gangguan.
- Gagal connect saat start atau saat reconnect sudah > 60 s:
  MQTT_CONNECT_FAILED, context {"rc": ..., "attempts": n, "down_s": n}.
- Pulih (`on_connect` rc == 0 setelah sempat putus): info
  MQTT_RECOVERED, category "MQTT", context {"down_s": n}.
- MQTT_DISABLED (info) HANYA kalau mode MQTT dipilih tetapi broker tidak
  dikonfigurasi. Di mode TCP jangan pernah kirim event MQTT_* apa pun:
  di Tongbao, MQTT_DISABLED dibaca dashboard sebagai gangguan.
- `on_disconnect` / `on_connect` berjalan di thread jaringan paho.
  Memanggil SDK dari sana aman (non-blocking), tetapi jangan melakukan
  I/O berat atau `flush()` di callback.

Mode TCP:
- Controller/palang tidak terjangkau (connect refused, timeout, socket
  ditutup, tidak ada balasan dalam batas waktu): PCLESS_DISCONNECTED,
  context {"transport":"tcp", "host": "...", "port": n, "reason":
  "refused|timeout|reset|no_reply", "retries": n}. Sekali per gangguan.
- Pulih (koneksi berhasil dan satu perintah/ack sukses): info
  PCLESS_RECOVERED, category "DEVICE", context {"down_s": n}.
- Perintah buka palang gagal walau koneksi tersambung: GATE_OPEN_FAILED
  (berlaku juga di mode MQTT).
- Balasan rusak/tidak dikenal dari controller: DEVICE_ERROR dengan
  context {"reason": "bad_reply", "len": n}. Jangan kirim isi bytes-nya.

Anti-goyang (debounce), berlaku untuk kedua mode:
- "Putus" baru dikirim setelah gagal terus-menerus ≥ 10 detik (sekali
  gagal lalu tersambung lagi dalam 10 detik = cukup log DEBUG).
- "Pulih" baru dikirim setelah tersambung stabil ≥ 30 detik.
- Kalau dalam 10 menit terjadi > 5 kali putus-sambung, kirim satu
  WARNING dengan event yang sama dan context {"flapping": true,
  "count": n}, lalu diam sampai stabil 10 menit.
- Implementasi satu kelas `Transition` yang dipakai MQTT, TCP, reader,
  printer, LPR, dan server pembayaran. Thread-safe (Lock), memakai
  `time.monotonic()` (jam dinding bisa melompat saat NTP sinkron).
- Log retry di jalur MQTT/TCP: level DEBUG, atau WARNING dengan
  `extra={"sentinel": False}`, supaya handler catch-all tidak
  menjadikannya event LOG_* berulang.

Gateway senyap (mode MQTT): broker tersambung, tetapi board/gateway
hardware tidak mengirim pesan apa pun selama ≥ N detik (N = ambang dari
tim Tongbao, simpan di config, default 120): GATEWAY_SILENT (HIGH),
context {"transport":"mqtt", "silent_s": n, "threshold_s": N}. Sekali per
kejadian; pulih saat pesan datang lagi → MQTT_RECOVERED dengan context
{"down_s": n, "after": "GATEWAY_SILENT"}. Kalau broker sendiri putus,
yang dikirim MQTT_DISCONNECTED, bukan GATEWAY_SILENT.

Ack perintah TCP: kalau controller mengirim balasan/ack, perintah buka
palang tanpa ack dalam batas waktu = GATE_OPEN_FAILED dengan context
{"reason": "no_ack", "timeout_ms": n}. Protokol kiosk ↔ controller tidak
diubah oleh integrasi ini; kalau controller tidak punya ack, catat di
laporan sebagai usulan untuk vendor controller.

Kirim juga `transport` ("mqtt"/"tcp") di heartbeat: SDK 0.4.0
`SentinelClient(..., transport=TRANSPORT)`. Agent v1.0.0 menampilkannya di
kartu gate ("Tongbao · MQTT"); agent lama mengabaikannya tanpa error.

## 6b. Stabilitas log di Lubuntu ARM

Perangkat ARM punya CPU kecil, RAM terbatas, dan SD card/eMMC yang aus
kalau terus ditulis. Integrasi wajib:
- Handler catch-all `_SentinelLoggingHandler.emit()` tidak boleh
  melempar exception, tidak boleh I/O jaringan langsung (hanya masuk
  antrean SDK), dan memakai guard re-entrancy (thread-local) supaya log
  dari dalam SDK tidak berputar.
- Pesan dipotong ke 1000 karakter; bytes dan data biner diubah menjadi
  ringkasan (`<bytes len=n>`), bukan hex mentah. Teks non-UTF-8 diganti
  (`errors="replace"`). Jangan pernah crash karena encoding locale
  (Lubuntu kadang berjalan dengan LANG=C).
- Antrean SDK terbatas (`queue_size` default) dan outbox SQLite dibatasi
  (`outbox_max_rows` 10000, `outbox_max_age_days` 7). Outbox di folder
  user, bukan di /tmp (hilang saat reboot) dan bukan di folder aplikasi
  yang ditimpa saat update.
- Log file aplikasi memakai `RotatingFileHandler` (mis. 5 MB × 5) kalau
  belum ada rotasi.
- Jam: banyak perangkat ARM tidak punya RTC, sehingga jam bisa 1970
  sampai NTP sinkron. Jangan memakai jam dinding untuk debounce atau
  backoff (pakai `time.monotonic()`). CLOCK_SKEW baru boleh dicek setelah
  uptime ≥ 10 menit, dan dikirim paling banyak sekali per start.
- Tidak ada polling tambahan khusus Sentinel. Heartbeat menumpang main
  loop; SDK sendiri membatasi kirim 1×/60 s dan menggabung event sama
  per 60 s.
- Target di perangkat asli: tambahan CPU dari Sentinel < 2 % rata-rata,
  RSS naik < 10 MB, dan tidak terus bertambah selama 24 jam.

## 7. Jaringan dan jam

- Kiosk dan server lokasi harus bisa saling menjangkau. Kalau lewat
  Tailscale, KEDUANYA wajib login ke akun/tailnet yang sama. Beda akun =
  timeout, apa pun isian aplikasinya (sudah terjadi di lapangan).
- Jam kiosk boleh salah; agent memakai waktu terima. Kalau heartbeat
  balasan `server_time` beda > 5 menit dari jam kiosk, catat sekali
  sebagai WARNING `CLOCK_SKEW` (context {"skew_s": n}).
- Jangan menambah konfigurasi firewall/Tailscale dari aplikasi.

## 8. Tes (unittest, lolos di Python 3.6 dan 3.12)

Pakai fake agent HTTP di localhost (http.server di thread):
- Validasi: Device ID `1`, Gate kosong, kunci kosong ditolak; spasi dan
  baris baru di kunci dibuang; `/` di akhir URL dibuang.
- Enroll 201 → token tersimpan, request berikutnya memakai
  `Authorization: Bearer <token>`; file config 0600.
- Enroll 401/404/409/400 → pesan yang tepat, aplikasi jalan dengan
  `_NoOpClient`, tanpa exception.
- Agent mati lalu hidup → kiosk terdaftar sendiri tanpa restart.
- Menyimpan form konfigurasi utama setelah pengaturan Sentinel →
  `SENTINEL_*` masih ada.
- Heartbeat membawa gate, gate_name, pos, platform="tongbao".
- Reader/PCLess retry 10× → event putus hanya 1.
- Log dan output tes tidak pernah memuat kunci lokasi atau token.
- Mode MQTT, dengan fake broker atau callback paho yang dipanggil
  langsung: putus 3 detik lalu sambung → tidak ada event; putus 15 detik
  → 1 MQTT_DISCONNECTED; sambung stabil 30 detik → 1 MQTT_RECOVERED; 20×
  putus-sambung dalam 10 menit → 1 event flapping saja.
- Mode TCP, dengan socket server palsu di localhost: refused, timeout,
  dan server menutup koneksi → masing-masing 1 PCLESS_DISCONNECTED dengan
  `reason` yang benar; pulih → 1 PCLESS_RECOVERED.
- Mode TCP tidak pernah mengirim MQTT_*. Mode MQTT tanpa broker
  terkonfigurasi → tepat 1 MQTT_DISABLED.
- Semua event hardware membawa `context.transport` yang benar.
- `Transition` memakai jam palsu (inject `monotonic`), jadi tes tidak
  perlu `sleep` panjang.
- Handler catch-all: pesan bytes, pesan 50 KB, teks non-UTF-8, dan
  exception di dalam emit → tidak ada crash, pesan terpotong.

Uji lapangan di Tongbao asli (ARM), satu kali per mode:
- Jangan kirim event uji ke agent lokasi yang sedang memantau kiosk
  sungguhan. Pakai agent uji terpisah (`sentinel-demo` atau agent di PC
  developer).
- Jalankan aplikasi 24 jam: catat jumlah event per jam, `outbox_pending`
  di dashboard (harus kembali 0), CPU dan RSS proses di awal dan akhir.
- Cabut kabel jaringan atau matikan broker/controller 1 menit, lalu
  pasang lagi: dashboard menampilkan tepat 1 event putus dan 1 event
  pulih, kartu gate kembali hijau.
- Matikan agent 10 menit lalu nyalakan: event selama mati tetap masuk
  (outbox), tidak ada yang hilang atau dobel.
- Reboot perangkat: aplikasi dan Sentinel jalan lagi tanpa disentuh.

## Serahkan

- Laporan audit langkah 0, termasuk tabel perangkat → transport →
  titik deteksi.
- Tabel akhir "event yang dikirim" per mode (MQTT dan TCP): nama event,
  kapan dikirim, severity, isi context.
- Hasil uji lapangan 24 jam per mode (angka, bukan "sudah dicoba").
- Diff per langkah, catatan CHANGELOG aplikasi.
- Daftar isian untuk teknisi di lapangan: Agent URL, Kunci lokasi,
  Device ID, Gate, Nama gate, POS.
- Cara uji di lapangan: setelah daftar, kartu gate muncul hijau di
  dashboard Sentinel dalam 1 menit; mematikan aplikasi membuat kartu
  kuning (STALE) lalu merah (DOWN) dalam beberapa menit.

Jangan mengubah protokol agent. Kalau menurutmu agent perlu field baru
(mis. `transport` di heartbeat), tulis sebagai usulan di laporan akhir.
```

---

Catatan untuk saya sendiri (bukan bagian prompt):

- Kunci lokasi ada di server lokasi: `cat ~/sentinel-local/site.key`.
- Agent URL saat ini: IP Tailscale server lokasi, port 8787. Server dan
  kiosk harus di tailnet yang sama. Kiosk `100.126.83.10` dan server BBD
  sudah berada di tailnet `rokibctdev@`.
