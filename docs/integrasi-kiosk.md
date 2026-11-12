# Integrasi SDK 0.2 ke aplikasi kiosk

Panduan memperbarui aplikasi gate-out (`pw-offstreet-cashless-…-v3`) dari SDK
0.1 (yang sudah di-vendor sebagai `app/sentinel/client.py`) ke SDK 0.2, untuk
MVP NOC pusat.

## Apa yang berubah dari 0.1

| | 0.1 | 0.2 |
|---|---|---|
| Agent mati / LAN atau WAN putus | event **dibuang** | event ditahan di outbox dan dikirim saat tersambung lagi |
| Pengiriman gagal berulang | dicoba lagi untuk setiap event (di Windows ±1 detik per event) | backoff 1 s → 60 s |
| Spam dari loop polling | 1 event per siklus | event pertama langsung dikirim, sisanya digabung jadi 1 ringkasan per 60 detik |
| Status kiosk di dashboard | tidak ada | `beat()` mengirim heartbeat; kalau aplikasi hang, gate menjadi STALE lalu DOWN |
| `on_send_error` | tidak pernah terpanggil | terpanggil untuk setiap kegagalan |

API lama (`error()`, `api_error()`, `pending()`) tetap ada dan tetap kompatibel.

## 1. Salin SDK

SDK sekarang terdiri dari beberapa file. Salin seluruh package-nya:

```
sdk/python/parkways_sentinel/  →  app/sentinel/parkways_sentinel/
    __init__.py  client.py  events.py  outbox.py
```

Di `app/sentinel/client.py` (factory `get_client()`), ubah import menjadi
`from .parkways_sentinel import SentinelClient`. `_NoOpClient` perlu
mendapat method `beat()` yang tidak melakukan apa-apa, supaya kill-switch tetap aman.

PyInstaller seharusnya otomatis menemukan package ini, sama seperti di 0.1.
Tetap periksa `Analysis-00.toc` untuk memastikan `outbox` dan `sqlite3` ikut ter-bundle.

## 2. Config baru

| Kunci | Contoh | Catatan |
|---|---|---|
| `SENTINEL_AGENT_URL` | `http://100.64.0.1:8787` | IP Tailscale **server lokal lokasi** (agent per lokasi, lihat [deploy-server-lokasi.md](deploy-server-lokasi.md)) |
| `SENTINEL_SITE_KEY` *(0.5)* | kunci lokasi dari teknisi | dipakai **sekali** untuk mendaftar sendiri (`enroll`). Sama untuk semua gate di lokasi itu |
| `SENTINEL_TOKEN` | diisi otomatis oleh `enroll()` | token milik kiosk ini. Boleh juga diisi manual dari `device add` (cara lama) |
| `SENTINEL_DEVICE_ID` | `PCC-G01-POS01` | unik per kiosk; dipakai sebagai nama device di dashboard |
| `SENTINEL_GATE` *(0.5)* | `G01` | kode gate pendek, **diatur dari pengaturan aplikasi** |
| `SENTINEL_GATE_NAME` *(0.5)* | `Gate Keluar Utara` | nama yang dibaca tim support di kartu gate |
| `SENTINEL_POS` *(0.5)* | `POS01` | opsional |
| `SENTINEL_OUTBOX_PATH` | `~/.bssparking/sentinel_outbox.db` | kosong berarti outbox hanya di memori (tidak bertahan saat aplikasi restart) |

### Daftar sendiri saat pertama jalan (`enroll`)

Teknisi tidak perlu lagi menjalankan `device add` dan menyalin token satu
per satu. Cukup isi `SENTINEL_SITE_KEY` (satu kunci per lokasi) dan biarkan
aplikasi mendaftar sendiri saat `SENTINEL_TOKEN` masih kosong:

```python
from .parkways_sentinel import SentinelClient, enroll, EnrollError

def get_client():
    cfg = load_kiosk_config()
    if not cfg.get("SENTINEL_AGENT_URL"):
        return _NoOpClient()
    if not cfg.get("SENTINEL_TOKEN") and cfg.get("SENTINEL_SITE_KEY"):
        try:
            res = enroll(cfg["SENTINEL_AGENT_URL"], cfg["SENTINEL_SITE_KEY"],
                         device_id=cfg["SENTINEL_DEVICE_ID"], gate=cfg["SENTINEL_GATE"],
                         pos=cfg.get("SENTINEL_POS"), gate_name=cfg.get("SENTINEL_GATE_NAME"),
                         platform="tongbao" if IS_TONGBAO else None, app_version=APP_VERSION)
        except EnrollError as exc:
            logger.warning("sentinel: pendaftaran gagal (%s); coba lagi saat start berikutnya", exc)
            return _NoOpClient()
        cfg["SENTINEL_TOKEN"] = res["token"]
        save_kiosk_config(cfg)          # token hanya diberikan sekali
        logger.info("sentinel: terdaftar sebagai %s %s di %s", res["location"], res["gate"], res["site_name"])
    return SentinelClient(
        device_id=cfg["SENTINEL_DEVICE_ID"],
        base_url=cfg["SENTINEL_AGENT_URL"],
        token=cfg["SENTINEL_TOKEN"],
        app_version=APP_VERSION,
        platform="tongbao" if IS_TONGBAO else None,
        gate=cfg.get("SENTINEL_GATE"),
        gate_name=cfg.get("SENTINEL_GATE_NAME"),
        pos=cfg.get("SENTINEL_POS"),
        outbox_path=os.path.expanduser(cfg["SENTINEL_OUTBOX_PATH"]) if cfg.get("SENTINEL_OUTBOX_PATH") else None,
        on_send_error=lambda exc, payload: logger.debug("sentinel: %s", exc),
    )
```

Aturan yang perlu dipegang:

- `enroll()` memblokir paling lama `timeout` (5 detik) dan **melempar
  `EnrollError`**; aplikasi tetap harus jalan tanpa Sentinel bila gagal
  (agent belum dipasang, kunci salah → HTTP 401, kunci lokasi tidak
  diaktifkan → 404). Jangan ulang dalam loop; cukup coba lagi saat start.
- Simpan token ke config lokal dengan izin file terbatas. Kalau token
  hilang (reinstall), panggil `enroll()` lagi: agent menerbitkan token baru
  dan yang lama otomatis mati.
- `gate`, `gate_name`, `pos` ikut setiap heartbeat. Mengubah nama gate dari
  pengaturan aplikasi langsung terlihat di dashboard dalam ≤ 1 menit, tanpa
  restart agent. Lokasi **tidak** diatur dari kiosk: selalu `site.code`
  agent yang menerimanya.
- Jika aplikasi menolak enroll (misal gate belum diberi nama), tampilkan
  itu di layar pengaturan, bukan di layar transaksi.

`on_send_error` sebaiknya ditulis ke log dengan level **debug**, jangan
warning. Handler catch-all `_SentinelLoggingHandler` meneruskan WARNING ke
Sentinel, sehingga log warning dari callback ini akan mengirim event baru
setiap kali pengiriman gagal, dan bisa berputar terus.

## 3. Heartbeat: `beat()` di main loop

Panggil `beat()` dari loop utama controller (`src/controller.py`) di setiap
siklus polling. Biayanya hanya satu assignment, dan SDK sendiri yang membatasi
pengiriman heartbeat maksimal sekali per 60 detik.

```python
while running:
    get_client().beat()
    ...  # polling reader, MQTT, dst.
```

Jangan panggil `beat()` dari thread terpisah atau timer. Heartbeat harus berhenti
kalau loop utama macet, karena dari situlah NOC tahu gate bermasalah.

## 4. Payment timeout / API error (prioritas MVP)

Saat ini baru `READER_NOT_READY` yang dikirim dari `app/api/payment.py`. Semua
pemanggilan HTTP ke server pembayaran perlu dibungkus seperti ini (sesuaikan
dengan library HTTP yang dipakai aplikasi):

```python
t0 = time.monotonic()
try:
    resp = session.post(url, json=body, timeout=PAYMENT_TIMEOUT)
except requests.Timeout:
    get_client().api_error(endpoint=path, status_code=None,
                           duration_ms=int((time.monotonic() - t0) * 1000),
                           transaction_ref=tx_ref)   # → HTTP_TIMEOUT (HIGH)
    raise
except requests.ConnectionError as exc:
    get_client().error("NETWORK_DISCONNECTED", endpoint=path, message=str(exc)[:200])
    raise
if resp.status_code >= 400:
    get_client().api_error(endpoint=path, status_code=resp.status_code,
                           duration_ms=int((time.monotonic() - t0) * 1000),
                           transaction_ref=tx_ref)   # 5xx → HTTP_SERVER_ERROR, 4xx → HTTP_CLIENT_ERROR
```

Yang **tidak boleh** dikirim: isi body request atau response, nomor kartu,
saldo, maupun kredensial. `transaction_ref` hanya berisi ID referensi.

## 4b. Exception dengan stack trace (untuk developer)

Di blok `except` yang selama ini hanya `logger.error(...)`, panggil
`exception()` supaya baris kode yang gagal terlihat di dashboard:

```python
try:
    fee = calc_fee(ticket)
except Exception:
    get_client().exception()            # event APP_EXCEPTION (HIGH) + traceback
    logger.info("calc_fee gagal")       # info, bukan error: hindari event ganda
```

Yang dikirim hanya nama file, fungsi, dan nomor baris per frame plus pesan
exception. Nilai variabel lokal **tidak pernah** dikirim. `exception(exc,
event="PAYMENT_TIMEOUT")` memakai nama event lain bila lebih tepat.

### Hindari event ganda

Satu kejadian jangan menghasilkan dua event. Kalau sudah ada panggilan
terstruktur (`error("GATE_OPEN_FAILED")`), log di sebelahnya pakai
`logger.info`, bukan `logger.error`, karena `_SentinelLoggingHandler`
meneruskan WARNING/ERROR sebagai event `LOG_*`. Catatan: agent menurunkan
`LOG_*` level WARNING ke LOW (tidak mewarnai gate); ERROR tetap HIGH.

### Platform

Kirim varian produk supaya NOC membaca event MQTT dalam konteks yang benar:

```python
SentinelClient(..., platform="tongbao")   # Tongbao: MQTT + hardware, Lubuntu ARM
# Windows/Linux: biarkan kosong, SDK mengisi dari sistem operasi.
```

### Event terstruktur untuk kondisi yang sering muncul

| Kondisi di lapangan | Event | Severity | Ganti log warning ini |
|---|---|---|---|
| PCLess menolak koneksi / tidak tersambung | `PCLESS_DISCONNECTED` | HIGH | `open_connection: PCLess ... menolak koneksi` (kirim sekali saat putus, sekali saat pulih, bukan tiap 15 detik) |
| Palang gagal dibuka | `GATE_OPEN_FAILED` | HIGH | sudah ada; log `api_open_gate` di sebelahnya jadikan `info` |
| Kamera LPR tidak pernah kirim | `LPR_NO_FEED` | LOW | `_refresh_lpr_indicator: Pill LPR merah` |
| QRIS belum dikonfigurasi | `QRIS_NOT_CONFIGURED` | LOW | `process_qris: XENDIT_QR_SECRET belum diisi` |
| MQTT sengaja dimatikan (Windows/Linux) | `MQTT_DISABLED` | INFO | `__init__: MQTT_PASSWORD tidak di-set` |

## 5. Cakupan 4 masalah prioritas

| Masalah | Event | Sumber di aplikasi |
|---|---|---|
| Reader kartu | `READER_DISCONNECTED`, `READER_NOT_READY`, `READER_INIT_FAILED` | sudah terpasang (controller, card_handler, aino, payment) |
| Gate/kiosk offline | heartbeat, lalu status DOWN | **baru**: `beat()` (langkah 3) |
| Payment timeout/API | `HTTP_TIMEOUT`, `HTTP_SERVER_ERROR`, `PAYMENT_TIMEOUT` | **baru**: langkah 4 |
| MQTT putus | `MQTT_DISCONNECTED`, `MQTT_CONNECT_FAILED` | sudah terpasang (`app/mqtt.py`) |

## 6. Uji di STI-G03 sebelum rollout

- [ ] Saat aplikasi jalan, gate tampil ONLINE di dashboard dengan versi aplikasi yang benar.
- [ ] Cabut reader: muncul `READER_*` dan gate menjadi merah. Pesan Telegram masuk satu kali, tidak berulang.
- [ ] Putuskan jaringan selama 5 menit, lalu sambungkan lagi: event selama putus muncul di dashboard dan ada `SENTINEL_GAP`.
- [ ] Hentikan aplikasi: gate berubah STALE setelah ±3 menit dan DOWN setelah ±10 menit, lalu alert "MATI" terkirim.
- [ ] Transaksi normal tidak melambat (SDK tidak pernah memblokir pemanggil).
- [ ] Picu satu exception uji: muncul `APP_EXCEPTION` dengan stack trace di detail event, dan "Salin untuk debugging" menghasilkan Markdown yang bisa ditempel.
- [ ] Kartu gate menampilkan platform (`Tongbao`/`Windows`) dan, saat aplikasi dihentikan, "Mati sejak HH.MM".
- [ ] Config baru tanpa `SENTINEL_TOKEN` + `SENTINEL_SITE_KEY` terisi: aplikasi mendaftar sendiri, token tersimpan di config, kartu muncul dengan nama gate dan `terdaftar sendiri lewat kunci lokasi` di halaman gate.
- [ ] Ubah `SENTINEL_GATE_NAME` dari pengaturan aplikasi: nama di kartu berubah dalam ≤ 1 menit.
- [ ] Kunci lokasi salah: aplikasi tetap melayani transaksi, log berisi `pendaftaran gagal (HTTP 401 …)`.
