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
| `SENTINEL_AGENT_URL` | `http://100.64.0.1:8787` | IP Tailscale VPS (sebelumnya `127.0.0.1`) |
| `SENTINEL_TOKEN` | dari `device add` | token baru dari agent VPS |
| `SENTINEL_DEVICE_ID` | `STI-G03-POS01` | harus sama dengan `-id` di `device add` |
| `SENTINEL_OUTBOX_PATH` *(baru)* | `~/.bssparking/sentinel_outbox.db` | kosong berarti outbox hanya di memori (tidak bertahan saat aplikasi restart) |

```python
SentinelClient(
    device_id=device_id,
    base_url=agent_url,
    token=token,
    app_version=APP_VERSION,
    outbox_path=os.path.expanduser(outbox_path) if outbox_path else None,
    on_send_error=lambda exc, payload: logger.debug("sentinel: %s", exc),
)
```

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
