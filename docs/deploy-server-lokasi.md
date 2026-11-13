# Deploy Sentinel Agent di server lokal tiap lokasi

Runbook pemasangan **satu agent per lokasi** (0.5+). Agent berjalan di
server lokal lokasi itu (PC yang sama dengan server parkir), hanya
memantau gate di lokasi tersebut, dan dibuka tim support lewat Tailscale.
Kiosk mendaftar sendiri memakai **kunci lokasi**, jadi teknisi tidak
menyalin token satu per satu.

```
PCC: server lokal ── sentinel-agent (site PCC) ◄── kiosk G01, G02 (LAN / Tailscale)
PJT: server lokal ── sentinel-agent (site PJT) ◄── kiosk G01..G04
                           │
   tim support ──Tailscale──┴── http://<IP-tailscale-server>:8787  (PCC · Plaza Central City)
```

Perbedaan dengan [deploy-vps.md](deploy-vps.md): tidak ada agent pusat,
`devices.yaml` tidak wajib, dan lokasi (`location`) semua event = `site.code`.

## 1. Beban dan kebutuhan

Agent diukur pada PC lokal (2026-11-12): **±16 MB RAM, <1 % CPU** saat
idle dengan heartbeat per menit, 32 MB pada 2,7 juta event. Satu event
±700 byte di disk. Dengan `retention.days: 30` dan `max_db_mb: 500`
database tidak akan melewati 500 MB apa pun yang terjadi di kiosk. Tidak
ada dependensi lain (binary tunggal, SQLite tertanam).

- Linux x86_64 atau Windows x86_64 (binary dari `deploy/build.sh`).
- Tailscale terpasang di server lokal; `tailscale ip -4` → misalnya `100.80.1.5`.
- Kiosk mencapai server lewat LAN atau Tailscale; pakai alamat yang sama di
  semua kiosk lokasi itu.

## 2. Pasang

Linux:

```bash
sudo useradd --system --home /opt/parkways-sentinel --shell /usr/sbin/nologin sentinel
sudo mkdir -p /opt/parkways-sentinel
sudo cp sentinel-agent-*-linux-amd64 /opt/parkways-sentinel/sentinel-agent
sudo cp agent/registry.yaml agent/config.example.yaml /opt/parkways-sentinel/
cd /opt/parkways-sentinel && sudo mv config.example.yaml config.yaml
sudo chown -R sentinel:sentinel /opt/parkways-sentinel
```

Windows: taruh `sentinel-agent-*-windows-amd64.exe`, `registry.yaml`, dan
`config.yaml` di `C:\ParkwaysSentinel\`, lalu daftarkan sebagai service
(NSSM atau `sc create`) dengan working directory folder itu.

## 3. `config.yaml` per lokasi

```yaml
site:
  code: "PCC"                      # kode lokasi, jadi prefix kode error PCC-G01-…
  name: "Plaza Central City"       # tampil di header dashboard
  enroll_key: ""                   # kosongkan di file; pakai env SENTINEL_SITE_KEY

server:
  listen_addr: "100.80.1.5:8787"   # IP Tailscale server lokal; kiosk LAN pakai IP LAN → bind "0.0.0.0:8787" hanya jika firewall membatasi ke LAN
  public_url: "http://100.80.1.5:8787"
  session_secret: "<openssl rand -hex 32>"

retention:
  days: 30
  max_db_mb: 500
```

Buat kunci lokasi sekali: `openssl rand -hex 16`. Simpan di environment
service (`Environment=SENTINEL_SITE_KEY=…` di unit systemd, atau variabel
service Windows), **bukan** di repo/chat. Kunci ini yang diberikan ke
teknisi untuk diisi di pengaturan aplikasi gateout lokasi itu. Kalau bocor,
ganti kuncinya dan restart agent: kiosk yang sudah terdaftar tidak
terpengaruh (mereka memakai token masing-masing).

Catatan `listen_addr`: kalau kiosk berada di LAN tanpa Tailscale, agent harus
mendengar di IP LAN juga. Pilihan paling sederhana `0.0.0.0:8787` dengan
firewall yang hanya mengizinkan subnet LAN dan 100.64.0.0/10.

## 4. User dashboard dan start

```bash
cd /opt/parkways-sentinel
sudo -u sentinel ./sentinel-agent user add -name support -role viewer
sudo -u sentinel ./sentinel-agent user add -name indra -role admin
```

Unit systemd `/etc/systemd/system/parkways-sentinel.service`:

```ini
[Unit]
Description=Parkways Sentinel Agent (site PCC)
After=network-online.target tailscaled.service

[Service]
User=sentinel
WorkingDirectory=/opt/parkways-sentinel
Environment=SENTINEL_SITE_KEY=<kunci lokasi>
ExecStart=/opt/parkways-sentinel/sentinel-agent -config config.yaml
Restart=always
RestartSec=5
Nice=10

[Install]
WantedBy=multi-user.target
```

`Nice=10` membuat agent mengalah pada aplikasi parkir bila CPU penuh.
Log start harus memuat `site PCC (Plaza Central City): enrollment on`.

## 5. Kiosk mendaftar sendiri

Di tiap kiosk cukup isi (lihat [integrasi-kiosk.md](integrasi-kiosk.md) §2):

```
SENTINEL_AGENT_URL = http://100.80.1.5:8787
SENTINEL_SITE_KEY  = <kunci lokasi>
SENTINEL_DEVICE_ID = PCC-G01-POS01
SENTINEL_GATE      = G01
SENTINEL_GATE_NAME = Gate Keluar Utara
```

Saat aplikasi start, SDK memanggil `POST /v1/enroll`, menyimpan token yang
diberikan, dan kartu gate muncul di dashboard dengan nama itu. Mengganti
nama/kode gate dari pengaturan aplikasi otomatis mengubah kartu dalam ≤ 1
menit. Cek dari server:

```bash
sudo -u sentinel ./sentinel-agent device list        # kolom terakhir: enrolled / devices.yaml
sudo -u sentinel ./sentinel-agent device revoke -id PCC-G02-POS01   # kiosk dibongkar
```

Kiosk yang di-reinstall tinggal start dengan `SENTINEL_TOKEN` kosong:
enroll ulang menerbitkan token baru dan yang lama mati.

## 6. Backup dan ruang disk

Agent sendiri menjaga ukuran: event lebih tua dari `retention.days`
dihapus harian, dan bila file DB melewati `max_db_mb` event tertua dibuang
lebih awal (tercatat di log sebagai peringatan, dan dashboard menampilkan
notifikasi kuning saat >80 % batas atau disk tersisa <1 GB).

Salinan harian tanpa sqlite3 terpasang:

```
15 2 * * * /opt/parkways-sentinel/sentinel-agent backup -config /opt/parkways-sentinel/config.yaml -out /opt/parkways-sentinel/backup/sentinel-$(date +\%a).db
```

`backup` memakai `VACUUM INTO`, aman dijalankan selagi agent hidup, dan
menolak menimpa file yang sudah ada. Yang perlu diamankan di luar server:
`config.yaml`, `users.yaml`, dan file DB (berisi hash token kiosk; kalau
hilang kiosk cukup enroll ulang).

## 7. Telegram: tidak dipakai

Keputusan 2026-11-13: notifikasi Telegram **tidak diaktifkan** (rawan
bocor/berisik; dashboard Sentinel sudah cukup). Biarkan
`SENTINEL_TELEGRAM_BOT_TOKEN` kosong dan `alert.routes` kosong — agent
mencatat `alerts off` saat start dan tidak membuka koneksi keluar sama
sekali. Insiden tetap tercatat dan tampil di dashboard. Kalau suatu saat
dibutuhkan, isi token lewat environment service dan satu `alert.routes`;
pesannya per insiden (satu pesan, diedit saat bertambah/selesai).

## 8. Dashboard pusat (belum ada — rencana)

Tiap lokasi berdiri sendiri; tim support membuka dashboard lokasi lewat
Tailscale (`http://<ip-tailscale-server>:8787`). Tahap berikutnya adalah
satu halaman pusat yang **hanya membaca** `/v1/fleet` setiap agent lokasi
lewat Tailscale dan menampilkan ringkasan semua lokasi (gate merah, insiden
aktif) dengan tautan ke dashboard lokasinya — tanpa memindahkan data, jadi
server lokasi tidak bertambah beban.

## 9. Cek akhir

- [ ] `curl http://100.80.1.5:8787/v1/health` → `{"status":"ok",…}`.
- [ ] Header dashboard menampilkan `PCC · Plaza Central City · agent 100.80.1.5:8787 · pendaftaran kiosk aktif`.
- [ ] Setiap kiosk tampil dengan nama gatenya, status ONLINE, platform benar.
- [ ] Kirim kunci salah dari satu kiosk: HTTP 401 di log kiosk, setelah beberapa kali 429, agent tetap sehat.
- [ ] Matikan satu kiosk 10 menit: kartu merah "Mati sejak" dan satu insiden aktif.
- [ ] `top`/Task Manager: `sentinel-agent` < 50 MB RAM, CPU ~0 saat idle.
- [ ] Biarkan satu kiosk berisik (mis. server pembayaran dimatikan) 1 jam: log agent memuat `squelch: … stored as one summary`, DB tidak membengkak, dashboard menampilkan satu insiden.
