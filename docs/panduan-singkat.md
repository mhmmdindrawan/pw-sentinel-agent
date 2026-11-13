# Panduan singkat Parkways Sentinel

**Cara kerjanya:** satu lokasi punya satu server. Sentinel dipasang di
server itu. Semua gate (kiosk) di lokasi itu melapor ke Sentinel. Tim
support melihat hasilnya di browser lewat Tailscale. Data hanya disimpan
untuk hari berjalan: setiap jam 02.00 data kemarin dihapus otomatis.

Contoh di bawah memakai PC uji: folder `~/sentinel-local`, IP Tailscale
`100.112.209.33`. Di lokasi lain, ganti IP-nya dengan hasil
`tailscale ip -4` di server lokasi itu.

---

## A. Pasang Sentinel di server (sekali per lokasi)

**1. Masuk ke folder Sentinel.** Isinya cukup tiga file:
`sentinel-agent`, `registry.yaml`, `config.yaml`.

```bash
cd ~/sentinel-local
```

**2. Isi identitas lokasi.** Buka `config.yaml`, ubah hanya bagian ini:

```yaml
site:
  code: "PCC"                 # kode lokasi, huruf besar. Muncul di depan setiap kode error.
  name: "Plaza Central City"  # nama lokasi yang dibaca tim support
  enroll_key: ""              # biarkan kosong (kunci diberikan lewat langkah 4)

server:
  listen_addr: "100.112.209.33:8787"
  public_url: "http://100.112.209.33:8787"
```

Bagian lain biarkan apa adanya.

**3. Buat akun untuk membuka dashboard.** Password diketik dan tidak
tampil di layar.

```bash
./sentinel-agent user add -name indra -role admin
./sentinel-agent user add -name support -role viewer   # tim support: hanya bisa melihat
```

**4. Buat kunci lokasi.** Kunci ini nanti diketik di setiap gate lokasi ini
supaya gate bisa mendaftar sendiri. Satu kunci untuk satu lokasi.

```bash
openssl rand -hex 16 > site.key
chmod 600 site.key
cat site.key        # catat untuk teknisi; jangan kirim lewat grup chat
```

**5. Coba jalankan.**

```bash
SENTINEL_SITE_KEY="$(cat site.key)" ./sentinel-agent -config config.yaml
```

Berhasil kalau muncul baris:

```
site PCC "Plaza Central City": 0 enrolled device(s), enrollment on
retention: monitoring data is wiped daily at 02:00 ...
Parkways Sentinel Agent ... listening on 100.112.209.33:8787
```

Buka `http://100.112.209.33:8787` lalu login. Di bawah judul tertulis
*PCC · Plaza Central City · agent 100.112.209.33:8787 · pendaftaran kiosk
aktif*. Hentikan dengan **Ctrl+C**.

**6. Jalankan otomatis saat PC menyala.**

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/sentinel.service <<'EOF'
[Unit]
Description=Parkways Sentinel
After=network-online.target

[Service]
WorkingDirectory=%h/sentinel-local
ExecStart=/bin/sh -c 'SENTINEL_SITE_KEY="$$(cat site.key)" exec ./sentinel-agent -config config.yaml'
Restart=always
RestartSec=5
Nice=10

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now sentinel.service
loginctl enable-linger "$USER"     # tetap jalan walau belum ada yang login
```

Melihat log: `journalctl --user -u sentinel -f`.
`Nice=10` membuat Sentinel mengalah kalau aplikasi gate butuh CPU.

---

## B. Daftarkan gate (di setiap kiosk)

Agent tidak perlu disentuh. Semua diisi dari kiosk.

1. Buka aplikasi gate, lalu **Konfigurasi → Parkways Sentinel →
   Pengaturan Sentinel**.
2. Isi:

| Kolom | Isi | Contoh |
|---|---|---|
| Agent URL | alamat Sentinel lokasi | `http://100.112.209.33:8787` |
| Kunci lokasi | isi `site.key` | `3f9a…` |
| Device ID | nama unik kiosk ini, tidak boleh sama | `PCC-G01-POS01` |
| Gate | kode gate | `G01` |
| Nama gate | nama yang dipakai orang | `Gate Keluar Utara` |
| POS | boleh kosong | `POS01` |

3. **Simpan**, lalu tutup dan buka lagi aplikasinya.
4. Status di layar pengaturan menjadi *Terdaftar sebagai PCC G01 (Plaza
   Central City)*. Dalam satu menit kartu gate muncul di dashboard.

**Menambah gate:** ulangi di kiosk baru dengan Device ID dan Gate yang
lain (`PCC-G02-POS01`, `G02`).
**Mengganti nama gate:** ubah di Pengaturan. Dashboard ikut berubah dalam
satu menit.
**Kiosk diinstal ulang:** isi lagi seperti di atas. Gate mendaftar ulang
sendiri dengan Device ID yang sama.

---

## C. Membaca dashboard

| Yang terlihat | Artinya |
|---|---|
| Kartu **hijau** | Gate normal |
| Kartu **kuning** | Laporan terlambat, ada gangguan sedang, atau event tertahan di kiosk |
| Kartu **merah** | Gate mati, belum pernah lapor, atau ada gangguan berat (palang, reader) |
| *Mati sejak 10.58 · 12 mnt* | Sejak kapan gate tidak memberi kabar |
| **Insiden aktif** | Satu baris per gate bermasalah: sejak kapan, berapa kali, apa saja. Hilang sendiri setelah gate normal 10 menit |
| Kolom **Masalah** | Penjelasan singkat (*Palang tidak terbuka*); nama teknisnya kecil di bawah |
| Kolom **Kode** | Kode pendek untuk disebut ke developer (`DEVICE-002`) |
| *Log aplikasi: …* | Catatan log dari aplikasi; tidak pernah membuat kartu merah |
| *Kiosk sempat tidak terhubung ke Sentinel* | Jaringan kiosk–server sempat putus; event selama putus tetap terkirim setelahnya |

Untuk melapor ke developer: buka event, klik **Salin untuk debugging**,
lalu tempel di chat.

---

## D. Data dan penghapusan harian

- Setiap **jam 02.00 (jam server)** event, insiden, dan riwayat hari
  sebelumnya dihapus. Gate yang terdaftar, akun, dan kode error tetap
  ada, jadi tidak ada yang perlu diulang.
- **Server mati saat jam 02.00** (misalnya mati jam 00.00, hidup jam
  07.00): data kemarin dihapus begitu server hidup jam 07.00. Event yang
  tertahan di kiosk selama server mati tetap masuk dan tidak ikut
  terhapus.
- Ingin menyimpan bukti sebelum terhapus? Pakai **Salin untuk debugging**,
  atau:

  ```bash
  ./sentinel-agent backup -out ~/bukti-$(date +%F).db
  ```

- Ingin mengganti jam hapus? Ubah `daily_reset_at` di `config.yaml`
  (kosongkan untuk mematikan), lalu jalankan
  `systemctl --user restart sentinel`.

---

## E. Kalau ada masalah

| Gejala | Cek |
|---|---|
| Dashboard tidak bisa dibuka | `systemctl --user status sentinel`; IP di `listen_addr` sama dengan `tailscale ip -4` |
| Pengaturan gate: *kunci lokasi salah* | Kunci yang diketik tidak sama dengan `site.key` |
| Pengaturan gate: *pendaftaran mandiri tidak aktif* | Sentinel dijalankan tanpa `site.key` (lihat langkah A6) |
| Pengaturan gate: *device_id sudah terdaftar statis* | Pakai Device ID lain |
| Kartu gate tidak muncul | Aplikasi gate sudah dibuka ulang setelah disimpan? Agent URL benar? |
| Kartu *Belum pernah lapor* | Gate terdaftar tapi aplikasinya belum berjalan |
| Mencabut gate yang dibongkar | `./sentinel-agent device revoke -id PCC-G02-POS01`, lalu restart Sentinel |
| Melihat daftar gate | `./sentinel-agent device list` |

---

## F. Aturan

- Jangan mengirim event uji ke Sentinel lokasi: hasilnya tercampur dengan
  laporan asli. Untuk uji, pakai `sentinel-demo`.
- `site.key`, `users.yaml`, `config.yaml`, dan file `.db` tidak boleh
  masuk git atau grup chat.
- Port 8787 hanya untuk Tailscale/LAN, jangan dibuka ke internet.
