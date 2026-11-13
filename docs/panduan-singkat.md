# Panduan singkat: memasang Parkways Sentinel di satu lokasi

Satu agent per lokasi, dipasang di server lokal lokasi itu. Semua kiosk
(gate) di lokasi itu melapor ke agent ini. Tim support membuka
dashboard-nya lewat Tailscale. Tidak ada Telegram.

Yang Anda butuhkan: binary `sentinel-agent` (dari `deploy/build.sh` atau
rilis GitHub), file `registry.yaml` dan `config.example.yaml` dari folder
`agent/`, dan aplikasi gateout versi ≥ 3.38 (sudah mendukung pendaftaran
sendiri).

## 1. Siapkan folder agent (5 menit)

```bash
mkdir -p ~/sentinel && cd ~/sentinel
cp /path/ke/sentinel-agent .            # binary sesuai OS server
cp /path/ke/registry.yaml .
cp /path/ke/config.example.yaml config.yaml
```

Edit `config.yaml`, cukup empat baris ini:

```yaml
site:
  code: "PCC"                    # kode lokasi, 3–4 huruf; jadi awalan semua kode error
  name: "Plaza Central City"     # nama yang dibaca tim support
server:
  listen_addr: "100.x.y.z:8787"  # IP Tailscale server ini (lihat: tailscale ip -4)
  public_url: "http://100.x.y.z:8787"
```

Sisanya biarkan default (retensi 30 hari, DB maksimal 500 MB, squelch 20,
insiden 10 menit, Telegram kosong = mati).

## 2. Buat kunci lokasi dan user dashboard

```bash
openssl rand -hex 16 > site.key && chmod 600 site.key   # kunci lokasi, satu per lokasi
./sentinel-agent user add -name support -role viewer    # password diketik, tidak tampil
./sentinel-agent user add -name indra   -role admin
```

Kunci lokasi (`site.key`) adalah yang Anda berikan ke teknisi untuk diisi
di tiap kiosk lokasi ini. Jangan tempel di chat.

## 3. Jalankan agent

Coba dulu di terminal:

```bash
SENTINEL_SITE_KEY="$(cat site.key)" ./sentinel-agent -config config.yaml
```

Baris log yang harus ada: `site PCC "...": enrollment on` dan
`listening on 100.x.y.z:8787`. Buka `http://100.x.y.z:8787`, login.
Header harus menampilkan `PCC · Plaza Central City · agent … ·
pendaftaran kiosk aktif`.

Kalau sudah benar, jadikan service (Linux: `deploy/parkways-sentinel.service`,
isi `Environment=SENTINEL_SITE_KEY=…` dan `WorkingDirectory=~/sentinel`;
Windows: NSSM/`sc create` dengan variabel lingkungan yang sama).

## 4. Daftarkan kiosk — dari kiosk, bukan dari agent

Di tiap kiosk buka layar **Pengaturan → Parkways Sentinel** (atau edit
`kiosk_config.json`) dan isi:

| Isian | Nilai |
|---|---|
| Agent URL | `http://100.x.y.z:8787` |
| Kunci lokasi | isi `site.key` |
| Device ID | unik per kiosk: `PCC-G01-POS01`, `PCC-G02-POS01`, … |
| Gate | `G01` |
| Nama gate | nama yang dipakai orang: `Gate Keluar Utara` |
| Token | **kosongkan** — terisi sendiri saat aplikasi start |

Restart aplikasi kiosk. Dalam ±1 menit kartu gate muncul di dashboard
dengan nama itu dan status Online. Mengganti nama gate di Pengaturan
langsung mengubah dashboard; tidak ada yang perlu dilakukan di agent.

Menambah gate baru = ulangi langkah ini di kiosk baru dengan Device ID
lain. Kiosk di-reinstall = kosongkan Token, start lagi.

## 5. Membaca dashboard

- **Ringkasan**: kartu per gate. Merah = mati / tidak pernah lapor /
  error berat; kuning = terlambat / error sedang / antrean; hijau = normal.
  "Mati sejak 10.58 · 12 mnt" menyebut sejak kapan.
- **Insiden aktif**: satu baris per gate yang sedang bermasalah: sejak
  kapan, berapa event, isinya apa (mis. *Pembaca kartu tidak siap ×5*).
  Selesai sendiri setelah gate online dan tenang 10 menit.
- **Halaman gate**: status, riwayat status, insiden, error per jam,
  ringkasan 24 jam, timeline event. Klik event → detail; **Salin untuk
  debugging** menghasilkan teks untuk ditempel ke chat/issue/AI.
- **Kolom Masalah** memakai nama manusia (*Palang tidak terbuka*) dengan
  nama teknis kecil di bawahnya; **Kode** pendek `DEVICE-002`
  (lengkapnya `PCC-G01-DEVICE-002` saat hover/detail).
- *Log aplikasi: …* adalah baris log kiosk yang diteruskan apa adanya:
  tidak pernah membuat gate merah; kalau mendominasi, halaman gate memberi
  peringatan "event paling berisik" untuk developer.

## 6. Perawatan

```bash
./sentinel-agent device list              # kiosk terdaftar (enrolled / devices.yaml)
./sentinel-agent device revoke -id PCC-G02-POS01   # kiosk dibongkar; restart agent
./sentinel-agent backup -out /backup/sentinel-$(date +%F).db   # salinan DB, aman saat agent hidup
```

Agent menjaga ukurannya sendiri (30 hari / 500 MB) dan memberi peringatan
di dashboard bila disk menipis. Beban: ±15–20 MB RAM, CPU ≈ 0.

## 7. Jangan

- Jangan menyuntik event uji ke agent lokasi; untuk uji pakai
  `sentinel-demo` di port lain. Data uji tidak bisa dibedakan dari laporan asli.
- Jangan commit `config.yaml`, `devices.yaml`, `users.yaml`, `site.key`,
  atau `*.db` ke git.
- Jangan buka port 8787 ke internet; cukup Tailscale/LAN.
