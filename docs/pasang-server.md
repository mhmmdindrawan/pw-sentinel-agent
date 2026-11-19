# Pasang Sentinel di Lokasi

Satu lokasi punya satu PC server. Sentinel dipasang di PC server itu, lalu
semua gate di lokasi tersebut melapor ke sana. PC server boleh **Linux**
atau **Windows**.

| | Linux | Windows |
|---|---|---|
| Folder Sentinel | `~/sentinel` (di folder home) | `D:\ParkwaysSentinel` |
| Cara pasang | `bash setup-lokasi.sh` | klik dua kali `setup-lokasi.bat` |

Ada 5 tahap:

1. Siapkan file
2. Kirim ke server
3. Pasang
4. Daftarkan gate
5. Cek hasil

---

## Tahap 1 — Siapkan file

*Dikerjakan di PC developer, di folder repo `parkways-sentinel-agent`.*

**1.1** Buat programnya:

```bash
deploy/build.sh v0.6.0
```

**1.2** Kumpulkan file sesuai jenis PC server:

**Server Linux** (4 file):

```bash
mkdir -p ~/kirim/sentinel
cp dist/sentinel-agent-v0.6.0-linux-amd64 deploy/setup-lokasi.sh \
   agent/registry.yaml agent/config.example.yaml ~/kirim/sentinel/
```

> PC server jenis ARM? Ganti `linux-amd64` dengan `linux-armv7`.
> Cara cek: ketik `uname -m` di PC server. Hasil `x86_64` = amd64,
> `armv7l` = armv7.

**Server Windows** (5 file):

```bash
mkdir -p ~/kirim/ParkwaysSentinel-setup
cp dist/sentinel-agent-v0.6.0-windows-amd64.exe deploy/setup-lokasi.bat \
   deploy/setup-lokasi.ps1 agent/registry.yaml agent/config.example.yaml \
   ~/kirim/ParkwaysSentinel-setup/
```

---

## Tahap 2 — Kirim ke server

**Linux:** folder `sentinel` harus sampai di folder home PC server
(`/home/<user>/sentinel`). Pilih salah satu:

- **Lewat Tailscale:**
  ```bash
  scp -r ~/kirim/sentinel user@100.x.x.x:~/
  ```
- **Lewat flashdisk:** salin folder `sentinel` ke folder home PC server.

**Windows:** salin folder `ParkwaysSentinel-setup` lewat flashdisk atau
share ke PC server, misalnya ke Desktop. Folder ini hanya berisi file
pemasangan. Sentinel-nya sendiri nanti terpasang di `D:\ParkwaysSentinel`.

---

## Tahap 3 — Pasang

**3.1** Pastikan PC server sudah login Tailscale dengan **akun yang sama
dengan kiosk**. Kalau akunnya beda, kiosk tidak bisa menghubungi server
(timeout).

### Server Linux

*Dikerjakan sebagai user biasa. **Jangan pakai `sudo`.***

```bash
tailscale ip -4                # harus muncul IP yang diawali 100.
cd ~/sentinel
bash setup-lokasi.sh
```

### Server Windows

Buka folder `ParkwaysSentinel-setup`, lalu klik dua kali
**`setup-lokasi.bat`**. Jawab **Yes** saat Windows meminta izin
Administrator.

> Tidak ada drive D, atau drive D adalah CD/DVD atau flashdisk? Setup akan
> meminta folder lain, misalnya `E:\ParkwaysSentinel`. Drive harus disk
> internal berformat NTFS.

### Lanjutan (Linux dan Windows sama)

**3.2** Jawab dua pertanyaan:

```
Nama lokasi: Plaza Central City
Kode lokasi, 2–10 huruf/angka [PCC]:      ← tekan Enter
```

**3.3** Tunggu sampai muncul **BERHASIL**, lalu catat dua hal ini:

```
Agent URL   : http://100.112.209.33:8787
Kunci lokasi: 3f9a1c...
```

> Kalau di bawah Agent URL muncul baris `atau http://nama-pc….ts.net:8787`,
> pakai alamat itu di kiosk bila bisa dibuka dari kiosk. Alamat itu tetap
> sama walau IP Tailscale berubah.

Selesai. Sentinel menyala sendiri setiap kali PC dihidupkan, dan pindah
sendiri ke IP baru kalau IP Tailscale PC ini berubah.

---

## Tahap 4 — Daftarkan gate

*Dikerjakan di setiap kiosk.*

**4.1** Buka **Konfigurasi → Parkways Sentinel → Pengaturan Sentinel**.

**4.2** Isi kolomnya:

| Kolom | Isi |
|---|---|
| Agent URL | dari Tahap 3.3 |
| Kunci lokasi | dari Tahap 3.3 |
| Device ID | beda tiap kiosk, contoh `PCC-G01-POS01` |
| Gate | contoh `G01` |
| Nama gate | contoh `Gate Keluar Utara` |

**4.3** Simpan, lalu tutup dan buka lagi aplikasinya.

---

## Tahap 5 — Cek hasil

**5.1** Dari laptop atau HP yang tersambung ke Tailscale, buka Agent URL di
browser.

**5.2** Login:

| Akun | Password | Bisa apa |
|---|---|---|
| `admin` | `Admin@1234` | semuanya |
| `teknisi` | `teknisi123` | hanya melihat |

**5.3** Dalam satu menit, kartu gate muncul. Kartu hijau berarti gate normal.

---

## Kalau mau update versi

Ulangi Tahap 1–3 dengan versi baru (Linux: salin file baru ke
`~/sentinel`; Windows: folder setup baru). Di Tahap 3.2, tekan **Enter**
dua kali. Data, gate, dan akun tidak hilang. Di Windows, versi lama
disimpan sebagai `sentinel-agent.exe.old`.

---

## Kalau ada masalah

| Muncul pesan | Lakukan |
|---|---|
| *jangan pakai sudo* (Linux) | Ulangi tanpa `sudo` |
| *jalankan sebagai Administrator* (Windows) | Klik dua kali `setup-lokasi.bat`, jangan `.ps1`, lalu pilih Yes |
| *Tailscale belum login* | Login Tailscale (Linux: `sudo tailscale up`), lalu ulangi |
| *binary tidak ditemukan* | File program belum disalin, atau salah jenis (amd64/armv7/windows) |
| *Folder … tidak bisa dipakai* (Windows) | Ketik folder di disk internal NTFS lain, misalnya `E:\ParkwaysSentinel` |
| *Agent belum menjawab* | Kirim log ke developer: Linux `journalctl --user -u sentinel -n 100`, Windows `D:\ParkwaysSentinel\logs\sentinel.log` dan `runner.log` |
| Dashboard tidak terbuka | Pastikan laptop tersambung ke Tailscale dengan akun yang sama |
| Dashboard dulu bisa, sekarang tidak | IP Tailscale PC server berubah. Cek alamat baru dengan `tailscale ip -4`, buka `http://<IP baru>:8787`, lalu ganti Agent URL di setiap kiosk |

---

## Perintah berguna di PC server

**Linux:**

| Untuk | Perintah |
|---|---|
| Cek Sentinel jalan | `systemctl --user status sentinel` |
| Nyalakan ulang | `systemctl --user restart sentinel` |
| Lihat kunci lokasi | `cat ~/sentinel/site.key` |
| Lihat log | `journalctl --user -u sentinel -f` |
| Cek penjaga IP | `systemctl --user status sentinel-ipwatch` |

**Windows** (PowerShell sebagai Administrator):

| Untuk | Perintah |
|---|---|
| Cek Sentinel jalan | `Get-ScheduledTask ParkwaysSentinel` (State: Running) |
| Nyalakan ulang | `Stop-ScheduledTask ParkwaysSentinel; Start-ScheduledTask ParkwaysSentinel` |
| Lihat kunci lokasi | `type D:\ParkwaysSentinel\site.key` |
| Lihat log | `Get-Content D:\ParkwaysSentinel\logs\sentinel.log -Tail 50 -Wait` |

> Server yang dipasang dengan versi lama memakai folder `~/sentinel-local`.
> Setup otomatis tetap memakai folder lama itu; ganti `~/sentinel` di tabel
> dengan `~/sentinel-local`.

---

**Jangan lupa:** kunci lokasi jangan dikirim lewat grup chat, dan port 8787
jangan dibuka ke internet.
