# Prompt: integrasi Parkways Sentinel 0.5 ke aplikasi gateout `pw-offstreet-cashless-v3`

Salin seluruh blok di bawah ke sesi Claude Code (atau asisten lain) yang
dibuka **di repo aplikasi gateout**. Lampirkan juga folder
`sdk/python/parkways_sentinel/` versi 0.3.0 dari repo Sentinel dan file
`docs/integrasi-kiosk.md` + `docs/protocol-v1.md` sebagai referensi.

---

```text
Kamu bekerja di repo aplikasi gateout parkir `pw-offstreet-cashless-v3`
(Python; berjalan di tiga platform: Tongbao = Lubuntu ARM + Python 3.6.9
dengan MQTT dan koneksi hardware, Windows x86_64, dan Linux x86_64).
Aplikasi ini sudah memakai SDK pemantauan Parkways Sentinel (folder
`app/sentinel/parkways_sentinel/`, factory `get_client()` di
`app/sentinel/client.py`, kill-switch `_NoOpClient`). Tugasmu: naikkan
integrasi ke SDK 0.3.0 / agent 0.5 dengan model **satu agent Sentinel per
lokasi** dan **kiosk mendaftar sendiri**. Jangan mengubah alur transaksi,
pembayaran, atau MQTT; Sentinel hanya mengamati.

Batasan keras:
- Python 3.6 harus tetap jalan di Tongbao: tanpa f-string, dataclass,
  walrus, `from __future__ import annotations`, atau typing yang butuh 3.7+.
  Hanya stdlib.
- SDK tidak boleh pernah memblokir thread transaksi. Satu-satunya panggilan
  yang boleh memblokir (maks 5 detik) adalah `enroll()` saat start.
- Jangan pernah mengirim data pelanggan ke Sentinel: nomor kartu, saldo,
  plat lengkap, nama, kredensial, isi payload pembayaran. Konteks event
  hanya kode/status/durasi.
- Jangan commit token/kunci. Config lokal kiosk (`kiosk_config.json`) tetap
  di luar git.

Langkah yang harus kamu kerjakan:

1. Vendor SDK 0.3.0. Ganti isi `app/sentinel/parkways_sentinel/` dengan
   paket terlampir (`__init__.py client.py events.py outbox.py`). Pastikan
   `from .parkways_sentinel import SentinelClient, enroll, EnrollError`
   berhasil dan `_NoOpClient` mempunyai method `beat()`, `error()`,
   `warning()`, `info()`, `exception()`, `close()` yang tidak melakukan
   apa pun.

2. Kunci config baru di `kiosk_config.json` (dengan default kosong, dan
   bisa diedit dari layar Pengaturan aplikasi yang sudah ada):
     SENTINEL_AGENT_URL   contoh http://100.80.1.5:8787 (server lokal lokasi)
     SENTINEL_SITE_KEY    kunci lokasi dari teknisi; dipakai sekali
     SENTINEL_DEVICE_ID   unik per kiosk, format LOKASI-GATE-POS, mis. PCC-G01-POS01
     SENTINEL_TOKEN       diisi otomatis setelah enroll, jangan diminta ke user
     SENTINEL_GATE        kode gate pendek, mis. G01
     SENTINEL_GATE_NAME   nama gate untuk tim support, mis. "Gate Keluar Utara"
     SENTINEL_POS         opsional, mis. POS01
     SENTINEL_OUTBOX_PATH default ~/.bssparking/sentinel_outbox.db
   Kunci lama (SENTINEL_AGENT_URL/TOKEN/DEVICE_ID) tetap kompatibel: kalau
   SENTINEL_TOKEN sudah terisi, enroll dilewati.

3. Enroll saat start, di `get_client()`:
   - Jika SENTINEL_AGENT_URL kosong → `_NoOpClient()`.
   - Jika SENTINEL_TOKEN kosong dan SENTINEL_SITE_KEY terisi → panggil
       enroll(agent_url, site_key, device_id=..., gate=..., pos=...,
              gate_name=..., platform=("tongbao" jika build Tongbao, selain
              itu None), app_version=APP_VERSION)
     Simpan `res["token"]` ke kiosk_config.json (tulis atomik: file
     sementara lalu os.replace; chmod 0600 di Linux). Log info
     "sentinel: terdaftar sebagai {location} {gate} di {site_name}".
   - Jika enroll melempar EnrollError → log WARNING sekali, kembalikan
     `_NoOpClient()`; aplikasi tetap jalan. HTTP 401 = kunci salah, 404 =
     agent belum mengaktifkan enroll, 409 = device_id sudah ada di
     devices.yaml agent (minta teknisi ganti SENTINEL_DEVICE_ID).
     Jangan retry dalam loop; cukup pada start berikutnya.
   - Jika agent menolak token lama saat kirim event (callback
     on_send_error menerima error 401) → kosongkan SENTINEL_TOKEN di
     config supaya start berikutnya enroll ulang. Jangan enroll ulang
     saat runtime.
   - Buat `SentinelClient(device_id, base_url, token, app_version,
     platform, gate, gate_name, pos, outbox_path, on_send_error)` dengan
     `on_send_error` yang menulis ke logger level DEBUG (bukan WARNING,
     supaya handler logging catch-all tidak membuat event berulang).

4. Identitas dinamis. `gate`, `gate_name`, `pos` dikirim SDK pada setiap
   heartbeat. Saat user mengubah nama/kode gate di layar Pengaturan,
   simpan config lalu panggil `get_client().set_identity(gate=...,
   gate_name=..., pos=...)` jika method itu ada di SDK; jika belum ada,
   tambahkan method kecil di SDK yang memperbarui `self._identity`
   (dict yang digabung ke payload heartbeat) secara thread-safe. Tidak
   perlu restart aplikasi.

5. Heartbeat tetap dari main loop controller (`get_client().beat()` tiap
   siklus polling, bukan dari thread/timer). Pastikan masih ada setelah
   perubahan ini.

6. Exception dengan stack trace. Di handler exception global
   (`sys.excepthook`, try/except paling luar di controller, dan handler
   thread worker) panggil `get_client().exception()` di dalam blok except.
   Jangan kirim exception yang sama dua kali (hapus logging ERROR yang
   sudah diteruskan handler catch-all `_SentinelLoggingHandler` untuk
   traceback itu, atau beri flag `extra={"sentinel": False}` agar handler
   melewatinya).

7. Event terstruktur, ganti log teks bebas pada kondisi berikut
   (nama event harus persis; context hanya nilai teknis):
     PCLESS_DISCONNECTED  pcless/palang tidak merespons        error(), context {"port": ..., "retries": n}
     GATE_OPEN_FAILED     perintah buka palang gagal           error(), context {"reason": ...}
     LPR_NO_FEED          kamera LPR tidak mengirim frame      warning(), context {"camera": ..., "seconds": n}
     QRIS_NOT_CONFIGURED  QRIS dipilih tapi merchant kosong    warning(), sekali per start
     MQTT_DISABLED        MQTT dimatikan lewat config          info(), sekali per start (hanya Tongbao)
     READER_*, MQTT_DISCONNECTED, PAYMENT_TIMEOUT, HTTP_TIMEOUT tetap seperti sekarang.
   Aturan anti-ganda: event "putus" dikirim saat transisi (sekali), bukan
   setiap retry; simpan state terakhir per komponen dan kirim lagi hanya
   bila kondisinya berubah atau sudah pulih (`info("..._RECOVERED")` tidak
   wajib).

8. Platform: build Tongbao memakai platform="tongbao" (deteksi dari flag
   build/konfigurasi yang sudah ada untuk mode MQTT+hardware), Windows dan
   Linux biarkan default SDK.

9. Layar Pengaturan: tampilkan status Sentinel read-only — "Terdaftar
   sebagai PCC G01 (Plaza Central City)" atau alasan gagal enroll — dan
   tombol "Daftar ulang" yang mengosongkan SENTINEL_TOKEN lalu memanggil
   enroll lagi. Jangan tampilkan token.

10. Uji (tulis sebagai test unit dengan fake agent HTTP di localhost, py3.6
    kompatibel):
    - config tanpa token + site key → POST /v1/enroll terjadi sekali,
      token tersimpan, client selanjutnya memakai Bearer token itu.
    - enroll 401 → aplikasi memakai _NoOpClient, tidak ada exception.
    - heartbeat membawa gate, gate_name, pos, platform.
    - set_identity() mengubah heartbeat berikutnya.
    - exception() mengirim event APP_EXCEPTION dengan field stack, tanpa
      nilai variabel lokal.
    - PCLESS_DISCONNECTED hanya terkirim sekali meski retry 10 kali.

11. Perbedaan per platform (satu kode, tiga build):
    - Linux x86 (PCLess/TCP): platform bawaan SDK "linux"; MQTT_DISABLED
      tidak dikirim. Path config/outbox ~/.local/share/<app>/ atau
      ~/.bssparking/, chmod 0600 untuk file yang memuat token.
    - Windows: platform bawaan "windows". Path memakai
      os.environ["LOCALAPPDATA"]; tidak ada chmod -- cukup folder milik
      user. Pastikan sqlite3 outbox tidak dikunci antivirus (gunakan
      nama file .db di folder user, bukan di Program Files).
    - Tongbao (Lubuntu ARM, Python 3.6.9, MQTT + hardware): WAJIB
      platform="tongbao" (deteksi dari flag/konfigurasi mode MQTT+hardware
      yang sudah ada, bukan dari platform.machine()). Kirim
      MQTT_DISCONNECTED/MQTT_CONNECT_FAILED (sudah ada) dan MQTT_DISABLED
      sekali per start bila MQTT dimatikan lewat config. Semua kode harus
      lolos di 3.6: tanpa f-string, dataclass, walrus, typing 3.7+.

12. Tahap 2 -- server pembayaran tidak terjangkau = SATU event transisi.
    Pengamatan 24 jam di kiosk nyata: log `cashless.server.request`,
    `cashless.server.health`, dan `cashless.open_connection` menghasilkan
    ±970 event/hari untuk satu kondisi (server 192.168.x.x tidak
    terjangkau), sementara event terstrukturnya hanya 25. Ubah menjadi:
    - Satu objek `Transition` untuk "server pembayaran" (bukan per
      fungsi): `down("HTTP_SERVER_ERROR" / "NETWORK_DISCONNECTED", ...)`
      saat permintaan pertama gagal, `up()` saat permintaan berikutnya
      berhasil -> kirim `info("SERVER_RECOVERED", context={"down_s": n})`.
    - Logging ERROR/WARNING di ketiga jalur itu diberi
      `extra={"sentinel": False}` supaya handler catch-all tidak
      meneruskannya lagi; cukup `logger.debug` untuk retry berikutnya.
    - Health check berkala yang gagal berulang TIDAK mengirim event lagi
      selama Transition masih `down`.
    - Target: satu gangguan server = <= 3 event (putus, pulih, dan
      mungkin satu PAYMENT_TIMEOUT yang nyata), bukan ratusan.
    - Pola yang sama untuk reader (READER_*), PCLess, dan kamera LPR:
      event saat berubah, bukan setiap siklus.
    Agent sendiri sejak 0.6 sudah menahan banjir (event sama >20×/jam
    diringkas, LOG_* maksimal MEDIUM), tetapi sinyal yang benar harus
    datang dari aplikasi.

Serahkan: diff yang rapi per langkah, catatan di CHANGELOG aplikasi, dan
daftar hal yang perlu diisi teknisi di lapangan (URL agent lokasi, kunci
lokasi, device id, gate, nama gate).
```

---

Catatan untuk saya sendiri (bukan bagian prompt): kunci lokasi dibuat di
server tiap lokasi (`openssl rand -hex 16`, env `SENTINEL_SITE_KEY`, lihat
[deploy-server-lokasi.md](deploy-server-lokasi.md)). Setelah APK gateout
mendukung enroll, `device add` hanya diperlukan untuk kiosk lama yang belum
di-upgrade.
