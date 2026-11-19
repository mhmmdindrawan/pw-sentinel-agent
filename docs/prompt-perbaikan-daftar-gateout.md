# Prompt: perbaikan pendaftaran Sentinel di aplikasi gateout

Salin semua isi di bawah garis ke Claude Code yang dibuka di repo aplikasi
gateout (`pw-offstreet-cashless-v3`; kode `app/sentinel/` dan
`src/frames/sentinel_settings.py` juga dipakai versi Tongbao).

**Temuan yang melatarbelakangi** (13 Nov 2026, build 236 / v3.38.0b3,
agent v0.6.0-rc3 lokasi BBD):

- Log aplikasi: `Sentinel aktif: agent=http://100.112.209.33:8787 device_id=1`.
  Tidak ada log `terdaftar sebagai …`, dan tidak ada log `gagal mendaftar`.
- Agent tidak menerima satu pun request dari kiosk: tidak ada `/v1/enroll`
  maupun heartbeat, dan `device list` kosong.
- Beberapa menit kemudian `kiosk_config.json` tidak berisi kunci `SENTINEL_*`
  sama sekali.

Kesimpulannya, pendaftaran tidak pernah dijalankan dan operator tidak diberi
tahu sebabnya.

---

Perbaiki alur pendaftaran Parkways Sentinel di aplikasi ini. Masalahnya:
operator mengisi **Pengaturan Sentinel**, menekan Simpan, tetapi kiosk tidak
pernah terdaftar dan tidak ada pesan apa pun.

Bukti dari lapangan (build 236): log menulis `Sentinel aktif:
agent=http://100.112.209.33:8787 device_id=1`, tanpa `terdaftar sebagai` dan
tanpa `gagal mendaftar`. Agent tidak menerima satu pun request. Setelah
beberapa menit, `kiosk_config.json` tidak lagi berisi kunci `SENTINEL_*`.
Dugaan: Kunci lokasi atau Gate kosong sehingga `_build_client` melewati
`_enroll` dan diam-diam jatuh ke mode "Aktif tanpa token (agent uji)". Selain
itu ada penulis lain `kiosk_config.json` yang menghapus kunci `SENTINEL_*`.
**Buktikan kedua dugaan ini di kode sebelum memperbaiki.**

## Kontrak agent (tidak berubah)

`POST {agent_url}/v1/enroll`, tanpa header Authorization:

```json
{"site_key":"…","device_id":"BBD-G01-POS01","gate":"G01","pos":"POS01",
 "gate_name":"Gate Keluar 1","platform":"linux","app_version":"3.38.0b3+build236"}
```

- Wajib: `site_key`, `device_id` (1–64 karakter `A–Z a–z 0–9 . _ -`), dan
  `gate` (karakter sama, di-uppercase oleh agent).
- Opsional: `pos` (karakter sama), `gate_name` (≤ 64), `platform`,
  `app_version`.
- `201` → `{"device_id","location","gate","pos","site_name","token"}`. Token
  hanya dikirim sekali; simpan sebagai `SENTINEL_TOKEN`.
- Error body selalu `{"error":"…"}`:

| Status | Arti |
|---|---|
| `400` | isian tidak valid |
| `401` | kunci lokasi salah |
| `404` | pendaftaran mandiri mati di agent |
| `409` | `device_id` statis di `devices.yaml` |
| `429` | terlalu banyak kunci salah |

- Cek agent hidup: `GET {agent_url}/v1/health` → `{"status":"ok","version":"…"}`.
  Tidak perlu login.
- Satu `device_id` = satu kiosk. Daftar ulang dengan `device_id` yang sama
  merotasi token, sehingga dua kiosk dengan ID sama (misalnya `1`) akan
  saling merebut token.

## Yang harus dikerjakan

1. **Validasi di jendela Pengaturan Sentinel sebelum Simpan.** Tampilkan
   pesan di jendela itu sendiri, jangan hanya di log:
   - **Agent URL:** wajib, harus diawali `http://` atau `https://` lalu
     host dan port. Hapus `/` di akhir.
   - **Kunci lokasi:** wajib selama belum ada token tersimpan. Buang spasi
     dan baris baru, karena operator menyalinnya dari terminal.
   - **Device ID:** wajib, pola di atas, minimal 3 karakter. **Tolak ID yang
     hanya angka** (`1`, `01`). Isi otomatis usulan `<GATE>-<POS>` dari
     kolom Gate/POS bila kosong, dan tampilkan contoh `BBD-G01-POS01`.
   - **Gate:** wajib, pola di atas, contoh `G01`.
   - **Nama gate:** opsional, ≤ 64 karakter. **POS:** opsional, pola di atas.

2. **Tombol "Tes koneksi"** di jendela yang sama: `GET /v1/health` dengan
   timeout 3 detik. Tampilkan `Agent OK (v0.6.0)` atau sebab gagal: URL
   salah, ditolak, timeout. Untuk timeout tambahkan saran "cek Tailscale".

3. **Simpan = daftar, dan hasilnya terlihat.** Setelah data valid tersimpan,
   jalankan enroll di thread latar. Jendela tetap terbuka dengan status
   "Mendaftar…". Tampilkan hasilnya di jendela:
   - `201`: `Terdaftar sebagai BBD G01 (BCT Baddoka)`, lalu jendela boleh
     ditutup.
   - Gagal: pesan bahasa Indonesia per status di atas, ditambah isi
     `error` dari agent. Jendela tetap terbuka supaya operator bisa
     membetulkan isian.
   - Agent tidak terjangkau: `Agent tidak bisa dihubungi — cek Agent URL dan
     Tailscale`.

4. **Hapus jalur diam-diam "Aktif tanpa token".** Kalau tidak ada token dan
   kunci lokasi kosong, status menjadi `Belum terdaftar: isi Kunci lokasi`.
   Catat di log sebagai WARNING, dan jangan buat client yang mengirim tanpa
   token, karena agent produksi akan menolaknya dengan 401. Kalau mode uji
   tanpa token masih perlu, aktifkan hanya lewat flag eksplisit, misalnya
   env `SENTINEL_ALLOW_NO_TOKEN=1`.

5. **Kunci `SENTINEL_*` tidak boleh hilang.** Telusuri setiap tempat yang
   menulis `kiosk_config.json`: `save_form_values`, `update_values`,
   "Restore konfigurasi", updater, dan penulis lain bila ada. Pastikan
   semuanya mempertahankan `SENTINEL_*`. Setelah Simpan di Pengaturan
   Sentinel, baca ulang file dan pastikan nilainya benar-benar tersimpan.
   Kalau tidak, tampilkan `Pengaturan gagal disimpan`. Tambahkan tes
   regresi: simpan form Konfigurasi utama setelah Pengaturan Sentinel, lalu
   `SENTINEL_*` harus tetap ada.

6. **Gagal karena jaringan → coba lagi sendiri.** Untuk timeout, connection
   refused, dan 5xx: enroll diulang di latar dengan jeda 10 s, 30 s, 60 s,
   lalu tiap 5 menit, tanpa perlu restart aplikasi. Untuk 400, 401, 404, dan
   409: berhenti dan tampilkan pesannya. Untuk 429: tunggu 5 menit.

7. **Log yang jelas, tanpa rahasia:**
   - `sentinel: mendaftar ke <url> sebagai <device_id> gate <gate>`
   - `sentinel: terdaftar sebagai …` atau `sentinel: gagal mendaftar (<status>): <error>`

   Jangan pernah mencatat kunci lokasi atau token.

8. **Status di layar Konfigurasi** (label Sentinel) memakai salah satu teks
   ini: `Terdaftar sebagai …`, `Mendaftar…`, `Gagal: …`,
   `Belum terdaftar: …`, `Mati (Agent URL kosong)`.

## Batasan

- Harus jalan di **Python 3.6** (Tongbao/Lubuntu). Jangan pakai f-string
  `=`, walrus, `dataclasses`, atau `subprocess.run(capture_output=…)`.
- Tanpa dependensi baru. `urllib` dari stdlib sudah cukup, atau pakai
  `app.sentinel.parkways_sentinel.enroll` yang sudah ada.
- UI Tkinter tidak boleh beku. Semua panggilan jaringan di thread latar, dan
  update label lewat `after()`.
- Jangan mengubah perilaku pembayaran, MQTT, atau reader.

## Selesai bila

- Device ID `1`, Gate kosong, atau Kunci lokasi kosong ditolak di jendela
  dengan pesan yang jelas.
- Isian benar ditambah agent hidup menghasilkan status `Terdaftar sebagai …`
  dalam beberapa detik, tanpa restart. Kartu gate juga muncul di dashboard
  agent dalam 1 menit.
- Kunci lokasi salah menampilkan `Kunci lokasi salah`.
- Agent mati menampilkan `Agent tidak bisa dihubungi…`, lalu kiosk terdaftar
  sendiri setelah agent hidup lagi.
- Restart aplikasi tetap terdaftar: token dan `SENTINEL_*` masih ada di
  `kiosk_config.json`.
- Tes otomatis untuk validasi, pemetaan status → pesan, dan pelestarian
  `SENTINEL_*` lolos di Python 3.6 dan 3.12.
