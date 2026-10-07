# Deploy Sentinel Agent ke VPS (MVP: NOC pusat)

Runbook untuk memasang satu agent pusat di VPS Linux. Semua kiosk mengirim
ke agent ini lewat Tailscale, sedangkan NOC membuka dashboard dan menerima
alert Telegram dari agent yang sama.

```
kiosk (SDK + outbox) ──Tailscale──► VPS: sentinel-agent ──► Telegram (NOC, pribadi)
                                         │
                                         └── dashboard http://<IP-tailscale>:8787
```

## 1. Prasyarat

- VPS Linux x86_64 (Ubuntu/Debian). Untuk satu lokasi, spesifikasi kecil
  sudah cukup: 1 vCPU, 1 GB RAM, dan 20 GB disk.
- Tailscale terpasang dan login ke tailnet yang sama dengan kiosk.
  `tailscale ip -4` menampilkan IP VPS, misalnya `100.64.0.1`.
- Binary `sentinel-agent-<versi>-linux-amd64` dari `deploy/build.sh` (folder `dist/`).

## 2. User dan folder

```bash
sudo useradd --system --home /opt/parkways-sentinel --shell /usr/sbin/nologin sentinel
sudo mkdir -p /opt/parkways-sentinel
sudo cp sentinel-agent-*-linux-amd64 /opt/parkways-sentinel/sentinel-agent
sudo cp agent/registry.yaml agent/config.example.yaml /opt/parkways-sentinel/
cd /opt/parkways-sentinel
sudo mv config.example.yaml config.yaml
sudo chown -R sentinel:sentinel /opt/parkways-sentinel
```

## 3. Konfigurasi (`config.yaml`)

| Kunci | Isi |
|---|---|
| `server.listen_addr` | **IP Tailscale VPS**: `100.64.0.1:8787`. Jangan pakai `0.0.0.0`. |
| `server.session_secret` | hasil `openssl rand -hex 32` |
| `server.public_url` | `http://100.64.0.1:8787` (dipakai sebagai link di pesan Telegram) |
| `status.*` | default: ONLINE ≤ 3 menit, STALE ≤ 10 menit |
| `retention.days` | default 90 hari |
| `alert.routes` | lihat langkah 6 |

## 4. User dashboard

Password dibaca dari stdin, jadi tidak tercatat di history shell.

```bash
cd /opt/parkways-sentinel
sudo -u sentinel ./sentinel-agent user add -name indra -role admin
sudo -u sentinel ./sentinel-agent user add -name noc -role viewer
```

## 5. Daftarkan kiosk

Jalankan satu perintah per gate. Token **hanya ditampilkan sekali**, jadi
langsung salin ke config kiosk tersebut (lihat
[integrasi-kiosk.md](integrasi-kiosk.md)).

```bash
sudo -u sentinel ./sentinel-agent device add -id STI-G01-POS01 -location STI -gate G01 -pos POS01
sudo -u sentinel ./sentinel-agent device list
```

Setelah menambah device, restart agent (`sudo systemctl restart parkways-sentinel`).

## 6. Telegram

1. Buat bot lewat **@BotFather** (`/newbot`), lalu simpan tokennya.
2. Masukkan bot ke grup NOC dan kirim satu pesan di grup itu. Ambil chat ID dari
   `https://api.telegram.org/bot<TOKEN>/getUpdates`. Chat ID grup diawali `-100…`.
   Untuk chat pribadi, kirim `/start` ke bot, lalu ambil chat ID dengan cara yang sama.
3. Simpan token di env file, bukan di `config.yaml`:
   ```bash
   echo 'SENTINEL_TELEGRAM_BOT_TOKEN=123456:ABC...' | sudo tee /etc/parkways-sentinel.env
   sudo chmod 600 /etc/parkways-sentinel.env
   ```
4. Isi rute di `config.yaml`:
   ```yaml
   alert:
     timezone: "Asia/Jakarta"     # Asia/Makassar untuk WITA
     routes:
       - name: noc
         chat_id: "-1001234567890"
         min_severity: HIGH        # gate MATI/pulih + error HIGH/CRITICAL
         locations: [STI]
       - name: pribadi
         chat_id: "123456789"
         min_severity: MEDIUM
   ```
5. Uji kirim:
   ```bash
   sudo -u sentinel env $(sudo cat /etc/parkways-sentinel.env) ./sentinel-agent alert test
   ```

## 7. systemd

```bash
sudo cp deploy/parkways-sentinel.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now parkways-sentinel
journalctl -u parkways-sentinel -f
```

Kalau Tailscale belum siap saat boot, bind ke IP Tailscale akan gagal.
systemd akan mencoba lagi setiap 5 detik (`Restart=on-failure`), jadi kondisi ini tidak perlu ditangani manual.

## 8. Firewall

Port 8787 hanya boleh dibuka dari Tailscale:

```bash
sudo ufw allow in on tailscale0 to any port 8787 proto tcp
sudo ufw deny 8787/tcp
```

## 9. Cek akhir

- [ ] `curl http://100.64.0.1:8787/v1/health` menampilkan `"status":"ok"`, versi yang benar, dan jumlah device.
- [ ] `curl http://<IP-publik-VPS>:8787/v1/health` **gagal**. Artinya port tidak terbuka ke internet.
- [ ] Dashboard bisa dibuka dari laptop NOC (lewat Tailscale) dan login berhasil.
- [ ] `alert test` sampai ke semua chat.
- [ ] Kiosk pertama berstatus ONLINE di dashboard dalam waktu 1–2 menit setelah aplikasinya berjalan.

## Operasional

**Upgrade.** Hentikan service, ganti file `sentinel-agent`, lalu start lagi.
Skema database diperbarui otomatis. Kiosk tidak kehilangan event selama agent mati,
karena SDK menahannya di outbox.

```bash
sudo systemctl stop parkways-sentinel
sudo cp sentinel-agent-<versi>-linux-amd64 /opt/parkways-sentinel/sentinel-agent
sudo systemctl start parkways-sentinel
```

**Backup harian** (contoh cron untuk user `sentinel`):

```
15 2 * * * sqlite3 /opt/parkways-sentinel/data/sentinel.db ".backup /opt/parkways-sentinel/backup/sentinel-$(date +\%a).db"
```

**Setelah agent restart**, alert status (gate MATI) ditahan selama satu periode
STALE (10 menit). Ini disengaja, karena semua kiosk tampak DOWN sampai
heartbeat berikutnya masuk.

**Pindah dari agent lama** (`100.71.251.17`): daftarkan ulang STI-G03 di VPS,
ganti `SENTINEL_AGENT_URL` dan `SENTINEL_TOKEN` di kiosk, pastikan statusnya
ONLINE di dashboard VPS, baru matikan service lama.
