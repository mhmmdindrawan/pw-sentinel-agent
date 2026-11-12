# Kasus lapangan → deteksi → uji

> Sumber kebenaran untuk backlog. Fitur baru hanya masuk kalau menjawab salah
> satu baris di sini. Kalau ada kasus baru di lokasi, **tambahkan barisnya
> dulu**, baru rencanakan fiturnya.

Setiap kasus punya tiga hal yang harus bisa diperiksa tanpa kiosk sungguhan:

- **Sinyal**: event/heartbeat yang sampai ke Sentinel (lihat `agent/registry.yaml`).
- **Deteksi yang diharapkan**: apa yang harus dilihat NOC di dashboard dan Telegram.
- **Skenario uji**: cara mereproduksinya memakai SDK asli terhadap `sentinel-demo`.
  Skenario adalah *definition of done* fitur yang menanganinya.

Ambang waktu yang dipakai di bawah mengikuti `config.example.yaml`:
ONLINE ≤ 3 menit, STALE ≤ 10 menit, selebihnya DOWN; cooldown alert 15 menit
per device + kode error; jendela error yang mewarnai gate 10 menit.

## Matriks

| # | Kasus di lokasi | Sinyal | Deteksi yang diharapkan | Status sekarang | Skenario uji |
|---|---|---|---|---|---|
| K1 | Reader kartu lepas / tidak menjawab | `READER_DISCONNECTED` (READER-001, HIGH), `READER_NOT_READY` (READER-003, HIGH), `READER_INIT_FAILED` (READER-004, MEDIUM) | Gate merah dalam ≤ 1 menit. **Satu** alert Telegram saat mulai, satu saat pulih, berapa pun siklus polling yang mengulang event-nya. | Deteksi dan warna: ada. Alert: cooldown 15 menit per kode, jadi event yang berulang > 15 menit mengirim alert lagi; tiga kode reader yang berbeda = tiga alert. Belum ada pengelompokan incident. | `reader_flapping`: kirim `READER_DISCONNECTED` tiap 5 detik selama 3 menit, lalu berhenti. Harapan: 1 gate merah, Telegram (mock) menerima tepat 1 pesan mulai + 1 pesan pulih. |
| K2 | Gate mati: listrik, app crash, PC hang | Heartbeat berhenti. Kadang `APP_CRASHED` (APP-002, CRITICAL) sempat terkirim, sering tidak. | STALE setelah 3 menit, DOWN setelah 10 menit, alert "gate mati" setelah grace period, alert "pulih" saat heartbeat kembali. Dashboard menampilkan **"Mati sejak HH:MM"** dan lama matinya. | Status, alert, "Mati sejak" dan riwayat status: **ada** (v0.4, `status_history`). Diuji lokal 2026-11-12: STALE tepat 3 menit, ONLINE 5 detik setelah start. | `gate_down_recover`: heartbeat normal 2 menit, diam 12 menit, heartbeat lagi. Harapan: urutan ONLINE→STALE→DOWN→ONLINE, 1 alert mati + 1 alert pulih, durasi mati tercatat. |
| K3 | Payment timeout / API pembayaran lambat | `PAYMENT_TIMEOUT` (PAY-002, HIGH), `HTTP_TIMEOUT` (API-002, HIGH), `HTTP_SERVER_ERROR` (API-001, HIGH), `HIGH_LATENCY` (NET-002, MEDIUM) via `api_error()` | Bedakan **satu gate** bermasalah (gate merah, alert gate) dari **semua gate di lokasi** bermasalah dalam jendela yang sama (artinya jaringan lokasi atau server pusat → alert satu kali per lokasi, bukan per gate). | Per gate: ada. `api_error()` **belum dipasang** di kiosk repo. Korelasi lintas gate: belum. | `payment_timeout_site_wide`: 4 device STI mengirim `PAYMENT_TIMEOUT` dalam 2 menit. Harapan: 1 alert bertuliskan "4 gate di STI", bukan 4 alert. Varian: hanya 1 device → alert per gate. |
| K4 | MQTT putus-sambung (flapping) | `MQTT_DISCONNECTED` (MQTT-001, MEDIUM), `MQTT_CONNECT_FAILED` (MQTT-002, HIGH) | Flapping tercatat sebagai 1 incident berisi hitungan ("putus 23× dalam 30 menit"), gate kuning, 1 alert dengan hitungan, bukan N alert. | Cooldown menahan sebagian spam, tapi hitungan dan incident belum ada. | `mqtt_flapping`: `MQTT_DISCONNECTED` tiap 60 detik selama 30 menit. Harapan: event tersimpan semua (untuk forensik), dashboard menampilkan hitungan, Telegram ≤ 2 pesan. |
| K5 | Internet lokasi putus total | **Tidak ada sinyal sama sekali** dari semua device di lokasi itu; saat pulih, SDK mengirim `SENTINEL_GAP` (SYS-901) + isi outbox. | Semua gate di lokasi STALE bersamaan → satu alert "lokasi STI tidak terjangkau", bukan 8 alert gate. Saat pulih, event yang tertahan masuk dengan `occurred_at` asli, bukan waktu kedatangan. | Alert per gate: ada (jadi 8 alert). Agregasi per lokasi: **belum**. `occurred_at` dipakai: ada. | `location_offline`: 5 device STI berhenti heartbeat serentak, 1 device LBI tetap normal. Harapan: 1 alert lokasi, LBI tidak tersentuh; setelah 15 menit semua kembali, timeline menunjukkan event tertunda pada waktu aslinya. |
| K6 | Agent/VPS tidak terjangkau dari kiosk (Tailscale putus, agent restart) | Dari sisi kiosk: `on_send_error`, outbox menumpuk. Dari sisi agent: tidak ada apa-apa sampai pulih, lalu `SENTINEL_GAP` + `outbox_backlog_threshold`. | **Aplikasi kiosk tidak pernah terganggu**: setiap panggilan SDK kembali < 2 detik, pembayaran tetap jalan. Tidak ada event hilang, urutan tetap, tidak ada duplikat setelah pulih. | Outbox, backoff, batch, idempotency: ada dan diuji unit. Uji end-to-end dengan pemutusan jaringan nyata: belum. | `agent_down_outbox`: agent dimatikan 10 menit sementara SDK mengirim 1 event/detik. Harapan: 600 event masuk setelah agent hidup, tidak ada duplikat, setiap `client.error()` selesai < 2 detik (ukur). Varian: blokir port dengan `iptables` (drop, bukan reject) untuk menguji timeout, bukan connection refused. |
| K12 | Error kode (exception) yang hanya terlihat di log kiosk | `APP_EXCEPTION` (APP-003, HIGH) dengan `stack` dari `client.exception()`. | Developer membuka detail event, melihat file/fungsi/baris yang gagal, menekan "Salin untuk debugging", dan menempelnya ke AI/issue tanpa harus SSH ke kiosk. | SDK 0.3 + agent + dashboard: **ada** (v0.4). Pemanggilan `exception()` di kiosk: belum. | `exception_visible`: picu exception uji di kiosk. Harapan: event HIGH dengan stack ≤ 30 frame, tanpa nilai variabel, tersalin sebagai Markdown. |
| K7 | Jam kiosk meleset (RTC lemah, NTP gagal) | `occurred_at` beda jam dari `received_at`. | Timeline event memakai `occurred_at`; **status ONLINE/STALE memakai `received_at`**, jadi gate tidak salah dianggap DOWN karena jamnya mundur. Dashboard menandai device yang selisih jamnya > 5 menit. | Dual timestamp: ada. Penanda selisih jam di dashboard: belum. | `clock_skew`: device mengirim `occurred_at` 2 jam lalu dengan heartbeat normal. Harapan: gate tetap ONLINE, ada penanda "jam kiosk meleset 2j". |
| K8 | Noise perilaku pelanggan dan log aplikasi | `INSUFFICIENT_BALANCE` (PAY-003, **MEDIUM**), `PAYMENT_REJECTED` (PAY-001, MEDIUM), catch-all `LOG_<logger>__<func>` dari `logging.warning` (**MEDIUM**) | **Tidak mengubah warna gate** dan tidak pernah jadi alert. Tetap tersimpan untuk analisis. | **Selesai v0.4**: `INSUFFICIENT_BALANCE`, `PAYMENT_REJECTED` → LOW; `LOG_*` level WARNING diturunkan ke LOW oleh agent. | `customer_noise`: 50 `INSUFFICIENT_BALANCE` dalam 10 menit. Harapan: gate tetap hijau, 0 alert, 50 event terlihat di tab event. |
| K9 | Satu lokasi punya banyak gate, hanya satu yang sering bermasalah | Event yang sama berulang di satu `device_id` dari hari ke hari. | Dashboard menjawab "gate mana yang paling sering bermasalah minggu ini" per lokasi, supaya teknisi tahu mana yang perlu diganti hardware-nya. | `/v1/summary` per kode ada. Peringkat per gate per rentang waktu: belum. | `repeat_offender`: 3 hari data demo, STI-G03 menyumbang 70% event READER. Harapan: ringkasan lokasi menampilkan STI-G03 di urutan pertama dengan persentasenya. |
| K11 | PCLess (kontroler palang) tidak tersambung: palang tidak bisa dibuka | Sekarang: `LOG_CASHLESS__OPEN_CONNECTION` tiap 15 detik + `GATE_OPEN_FAILED` (DEVICE-002, HIGH) saat ada mobil. Target: `PCLESS_DISCONNECTED` (DEVICE-003, HIGH) sekali saat putus, sekali saat pulih. | Gate merah saat PCLess putus **sebelum** ada mobil yang tertahan; satu alert mulai + satu pulih. | Terlihat di uji lokal 2026-11-12: 8 event "menolak koneksi" per 2 menit (LOW sejak v0.4) dan `GATE_OPEN_FAILED` ganda dengan `LOG_CASHLESS__API_OPEN_GATE`. Event terstruktur **belum dipasang di kiosk**. | `pcless_down`: matikan PCLess 2 menit lalu hidupkan. Harapan: 1 `PCLESS_DISCONNECTED` + 1 pulih, 0 event `LOG_*` untuk kejadian yang sama. |
| K10 | Bug yang sama muncul di beberapa gate setelah update aplikasi | Event baru/aneh dengan `app_version` yang sama di banyak device. | Terlihat "event X muncul pertama kali di versi 3.4.1, di 5 gate, 2 jam setelah update". | `app_version` tersimpan. Fingerprint dan "pertama muncul di versi" belum (lihat `rencana-ai-debugging.md`). | `new_bug_after_update`: 5 device pindah `app_version` lalu mengirim event baru. Harapan: dashboard menandai event baru + versi pertama kemunculan. |

## Uji ketahanan (setiap rilis, bukan setiap PR)

Dijalankan manual atau terjadwal terhadap staging, hasilnya dicatat di PR rilis.

| Uji | Harapan |
|---|---|
| Agent `kill -9` di tengah beban 50 ev/s | `PRAGMA integrity_check` = ok, jumlah event yang di-ack = jumlah tersimpan |
| Disk VPS penuh (`fallocate` sampai habis) | Agent menolak tulis dengan error jelas di log, SDK menahan di outbox, tidak ada korupsi; pulih sendiri setelah disk lega |
| Telegram 429 lalu mati 30 menit | Antrean tersimpan, urutan tetap, tidak ada pesan dobel setelah restart agent, pesan > 2 menit ditandai "terlambat terkirim" |
| Beban 2,7 juta event (100 gate × 90 hari) | Batch 50 event < 200 ms, `/v1/fleet` p50 < 100 ms. Ambang ini **regresi**, bukan catatan |
| Tailscale VPS belum siap saat boot | Agent retry bind sampai IP Tailscale ada (lihat `deploy-vps.md`), tidak crash loop |

## Ukuran keberhasilan canary STI (2 minggu pertama)

- **Presisi alert** = alert yang benar perlu tindakan ÷ semua alert. Target ≥ 80 %.
  Di bawah itu: kurangi noise (K8, K1, K4) sebelum menambah gate.
- **Waktu deteksi** gate mati = alert Telegram − waktu gate benar-benar mati
  (dari laporan petugas). Target < 5 menit.
- Catat setiap alert di spreadsheet: waktu, gate, kode, benar/palsu, tindakan.
  Data ini yang menentukan apakah fitur AI diagnosis layak dibangun.

## Urutan pengerjaan yang disarankan

1. ~~K8 severity LOW~~, ~~K2 "Mati sejak"~~, ~~K12 stack trace~~: selesai v0.4.
2. **Kiosk repo**: pasang `PCLESS_DISCONNECTED` (K11), `exception()` (K12),
   `platform="tongbao"`, dan hilangkan event ganda. Butuh akses source
   `pw-offstreet-cashless-v3`.
3. K6 skenario `agent_down_outbox` dengan pemutusan jaringan nyata (uji lokal
   2026-11-12 lulus untuk agent mati; `iptables` drop belum).
4. K5 dan K3 agregasi per lokasi, lalu K1/K4 pengelompokan incident. Desainnya
   ditentukan **setelah** canary memberi pola error nyata.
5. K9, K10, K7 menyusul.
