#!/usr/bin/env bash
# Setup cepat Parkways Sentinel di server lokasi (Linux, systemd --user).
# Jalankan sebagai user biasa, JANGAN pakai sudo.
#
# Di server lokasi: salin skrip ini, binary sentinel-agent-<versi>-linux-amd64
# (atau -linux-armv7), registry.yaml dan config.example.yaml ke folder
# ~/sentinel, lalu jalankan `cd ~/sentinel && bash setup-lokasi.sh` dan ketik
# nama lokasi. Sentinel terpasang di folder itu juga (~/sentinel).
#
#   bash setup-lokasi.sh [-n "NAMA LOKASI"] [-c KODE] [-d FOLDER] [-p PORT] [-b BINARY]
#   deploy/setup-lokasi.sh --sync-ip [-d FOLDER]     # dipakai service sebelum start
#   deploy/setup-lokasi.sh --watch-ip [-d FOLDER]    # dipakai service sentinel-ipwatch
#   tambah --no-service untuk hanya menyiapkan file (tanpa systemd)
#
# Yang dilakukan:
#   1. Ambil IP Tailscale PC ini dari `tailscale ip -4` dan pastikan IP itu
#      benar-benar terpasang di interface PC (bukan IP contoh / IP PC lain).
#   2. Salin binary + registry.yaml + config.yaml dari folder skrip ini (atau
#      dari repo: build terbaru di dist/). Versi baru menggantikan yang lama.
#   3. Isi config.yaml: site.code, site.name, listen_addr, public_url,
#      session_secret (dibuat sekali kalau masih kosong).
#   4. Buat site.key (kunci lokasi) kalau belum ada.
#   5. Buat akun dashboard standar admin & teknisi kalau belum ada.
#   6. Pasang & nyalakan systemd --user service `sentinel`, yang setiap start
#      menyamakan ulang IP di config.yaml dengan IP Tailscale saat itu.
#   7. Pasang service `sentinel-ipwatch`: kalau IP Tailscale PC ini berubah
#      saat Sentinel sedang jalan, Sentinel dinyalakan ulang di IP yang baru
#      (paling lama 1 menit).
#
# Aman dijalankan ulang: yang sudah ada (kunci, akun, data) tidak ditimpa.
set -euo pipefail
# Jangan pernah berhenti diam-diam: tampilkan baris yang gagal.
trap 'rc=$?; echo "GAGAL: skrip berhenti di baris $LINENO (kode $rc). Kirim layar ini ke developer." >&2' ERR

DIR="${HOME}/sentinel" DIR_SET=0
CODE="" NAME="" PORT="8787" BIN="" SYNC_ONLY=0 WATCH_IP=0 NO_SERVICE=0

die()  { echo "GAGAL: $*" >&2; exit 1; }
info() { echo "==> $*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    -d) DIR="$2"; DIR_SET=1; shift 2 ;;
    -c) CODE="$2"; shift 2 ;;
    -n) NAME="$2"; shift 2 ;;
    -p) PORT="$2"; shift 2 ;;
    -b) BIN="$2"; shift 2 ;;
    --sync-ip) SYNC_ONLY=1; shift ;;
    --watch-ip) WATCH_IP=1; shift ;;
    --no-service) NO_SERVICE=1; shift ;;
    -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
    *) die "opsi tidak dikenal: $1 (lihat --help)" ;;
  esac
done

# --- IP Tailscale -----------------------------------------------------------

in_cgnat() { # 100.64.0.0/10 = 100.64.x.x – 100.127.x.x
  local a b
  IFS=. read -r a b _ _ <<<"$1"
  [ "$a" = 100 ] && [ "$b" -ge 64 ] && [ "$b" -le 127 ]
}

on_this_pc() { ip -4 -o addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | grep -qx "$1"; }

# Menunggu sampai Tailscale siap (saat boot service bisa start lebih dulu).
tailscale_ip() {
  command -v tailscale >/dev/null || die "perintah tailscale tidak ada. Pasang Tailscale dan login dulu (sudo tailscale up)."
  local ip="" i tries="${1:-1}"
  for i in $(seq 1 "$tries"); do
    ip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
    if [ -n "$ip" ] && in_cgnat "$ip" && on_this_pc "$ip"; then
      echo "$ip"; return 0
    fi
    if [ "$i" -lt "$tries" ]; then sleep 2; fi
  done
  [ -n "$ip" ] || die "Tailscale belum login / belum terhubung (cek: tailscale status)."
  in_cgnat "$ip" || die "IP dari tailscale ($ip) bukan IP Tailscale 100.64.0.0/10."
  die "IP Tailscale $ip tidak terpasang di interface PC ini (cek: ip -4 addr show tailscale0)."
}

cfg_get() { sed -n "s/^  $1: *\"\{0,1\}\([^\"#]*\)\"\{0,1\}.*/\1/p" "$CFG" | head -n1 | sed 's/ *$//'; }
cfg_set() { # cfg_set key value  (key di-indent 2 spasi, unik di config.yaml)
  grep -q "^  $1:" "$CFG" || die "kunci '$1' tidak ada di $CFG"
  local v; v="$(printf '%s' "$2" | sed 's/[\\&|]/\\&/g')"
  sed -i "s|^  $1:.*|  $1: \"$v\"|" "$CFG"
}

sync_ip() { # sync_ip percobaan [port]  (tanpa port: pakai port di config)
  local ip port want_listen want_url
  ip="$(tailscale_ip "${1:-1}")"
  port="${2:-$(cfg_get listen_addr | sed -n 's/.*:\([0-9]*\)$/\1/p')}"
  port="${port:-8787}"
  want_listen="$ip:$port" want_url="http://$ip:$port"
  if [ "$(cfg_get listen_addr)" != "$want_listen" ] || [ "$(cfg_get public_url)" != "$want_url" ]; then
    cfg_set listen_addr "$want_listen"
    cfg_set public_url "$want_url"
    echo "IP Tailscale disamakan: $want_listen"
  fi
  TS_IP="$ip"
}

CFG="$DIR/config.yaml"

if [ "$SYNC_ONLY" = 1 ]; then
  [ -f "$CFG" ] || die "$CFG tidak ada"
  sync_ip 30          # tunggu Tailscale sampai ±60 detik saat boot
  exit 0
fi

# Agent hanya mendengarkan di IP Tailscale yang berlaku saat ia start. Kalau
# IP itu berubah (PC login ulang ke Tailscale, IP diganti dari admin console),
# agent tetap hidup tetapi tidak bisa dihubungi siapa pun. Penjaga ini
# membandingkan IP Tailscale dengan config.yaml tiap menit dan menyalakan
# ulang Sentinel kalau berbeda; --sync-ip di awal start yang menulis IP baru.
if [ "$WATCH_IP" = 1 ]; then
  [ -f "$CFG" ] || die "$CFG tidak ada"
  every="${SENTINEL_IPWATCH_SECONDS:-60}"
  while sleep "$every"; do
    now_ip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
    # Tailscale mati atau belum siap: biarkan, jangan ganggu Sentinel.
    { [ -n "$now_ip" ] && in_cgnat "$now_ip" && on_this_pc "$now_ip"; } || continue
    cfg_ip="$(cfg_get listen_addr)"; cfg_ip="${cfg_ip%:*}"
    [ "$now_ip" != "$cfg_ip" ] || continue
    echo "IP Tailscale berubah: ${cfg_ip:-?} -> $now_ip. Sentinel dinyalakan ulang."
    systemctl --user restart sentinel.service || echo "gagal menyalakan ulang Sentinel; dicoba lagi $every detik lagi"
  done
  exit 0
fi

# --- Folder & file ----------------------------------------------------------

if [ "$(id -u)" = 0 ]; then
  die "jangan pakai sudo/root. Jalankan sebagai user biasa${SUDO_USER:+ ($SUDO_USER)}, contoh: bash $(basename "$0")"
fi

echo "Parkways Sentinel — pemasangan server lokasi"
echo

# Alat pendukung. Kalau belum ada, dipasang lewat apt (minta password sudo).
missing=""
for c in curl openssl tar; do command -v "$c" >/dev/null || missing="$missing $c"; done
if [ -n "$missing" ]; then
  info "Memasang alat pendukung:$missing"
  sudo apt-get update -qq && sudo apt-get install -y -qq $missing || die "gagal memasang$missing. Coba: sudo apt install -y$missing"
fi
command -v systemctl >/dev/null || die "PC ini tidak memakai systemd; pasang manual (lihat docs/pasang-server.md)"

# Cek Tailscale di awal, sebelum bertanya apa pun.
TS_IP="$(tailscale_ip 1)"
info "Tailscale OK, IP PC ini: $TS_IP"

case "$(uname -m)" in
  x86_64) ARCH=linux-amd64 ;;
  armv7l|armv6l) ARCH=linux-armv7 ;;
  *) ARCH="" ;;
esac
pick() { local f; for f in "$@"; do [ -f "$f" ] && { echo "$f"; return 0; }; done; return 0; }

# File diambil dari folder skrip ini (server lokasi) atau dari repo
# (deploy/setup-lokasi.sh dengan build di dist/).
SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
SELF_DIR="$(dirname "$SELF")"
REPO="$(cd "$SELF_DIR/.." && pwd)"
newest() { local f; f="$(ls -t "$@" 2>/dev/null | grep -v -- '-rc[0-9]' | head -n1 || true)"; [ -n "$f" ] || f="$(ls -t "$@" 2>/dev/null | head -n1 || true)"; echo "$f"; }

# Binary: -b, atau sentinel-agent-*-<jenis PC> di folder skrip ini, atau
# build terbaru di dist/ repo.
case "$BIN" in /*|"") ;; *) BIN="$PWD/$BIN" ;; esac
if [ -z "$BIN" ] && [ -n "$ARCH" ]; then
  BIN="$(newest "$SELF_DIR"/sentinel-agent-*-"$ARCH")"
  [ -n "$BIN" ] || BIN="$(newest "$REPO"/dist/sentinel-agent-*-"$ARCH")"
fi
if [ -z "$BIN" ] && [ -z "$ARCH" ]; then
  die "jenis PC ini ($(uname -m)) belum didukung"
fi

# Sudah pernah dipasang (mis. di ~/sentinel-local versi lama) dan -d tidak
# diberikan: pakai folder pemasangan lama, jangan buat pemasangan kedua.
UNIT="$HOME/.config/systemd/user/sentinel.service"
if [ "$DIR_SET" = 0 ] && [ -f "$UNIT" ]; then
  old_dir="$(sed -n 's/^WorkingDirectory=//p' "$UNIT" | head -n1)"
  if [ -n "$old_dir" ] && [ "$old_dir" != "$DIR" ] && [ -f "$old_dir/config.yaml" ]; then
    DIR="$old_dir"
    info "Sudah pernah dipasang di $DIR; folder itu yang dipakai"
  fi
fi

mkdir -p "$DIR"
cd "$DIR"
CFG="$DIR/config.yaml"
info "Folder: $DIR"

if [ -n "$BIN" ] && ! cmp -s "$BIN" sentinel-agent; then
  [ -e sentinel-agent ] && UPGRADED=1
  install -m 755 "$BIN" sentinel-agent.new && mv -f sentinel-agent.new sentinel-agent
  info "Binary: $(./sentinel-agent version 2>/dev/null || basename "$BIN")${UPGRADED:+ (diperbarui)}"
fi
[ -x sentinel-agent ] || die "binary tidak ditemukan. Taruh sentinel-agent-<versi>-$ARCH di folder yang sama dengan skrip ini ($SELF_DIR)"
# registry.yaml dari versi baru menggantikan yang lama (cadangan: .bak).
NEW_REG="$(pick "$SELF_DIR/registry.yaml" "$REPO/agent/registry.yaml")"
if [ -n "$NEW_REG" ] && [ -f registry.yaml ] && ! cmp -s "$NEW_REG" registry.yaml; then
  cp registry.yaml registry.yaml.bak && cp "$NEW_REG" registry.yaml
  info "registry.yaml diperbarui (yang lama: registry.yaml.bak)"
fi
for f in registry.yaml config.yaml; do
  [ -f "$f" ] && continue
  if [ "$f" = config.yaml ]; then
    src="$(pick "$SELF_DIR/config.example.yaml" "$REPO/agent/config.example.yaml")"
  else
    src="$(pick "$SELF_DIR/registry.yaml" "$REPO/agent/registry.yaml")"
  fi
  [ -n "$src" ] || die "${src:-$f} tidak ditemukan. Salin registry.yaml dan config.example.yaml ke $SELF_DIR"
  cp "$src" "$f"; info "$f disiapkan"
done
chmod 600 config.yaml

# --- Identitas lokasi -------------------------------------------------------

# Sudah pernah dipasang → nama & kode lama jadi pilihan bawaan (tekan Enter).
# Pemasangan baru → config.yaml masih berisi contoh, jangan ditawarkan.
cur_code="" cur_name=""
if [ -f site.key ]; then cur_code="$(cfg_get code)" cur_name="$(cfg_get name)"; fi

# Usulan kode dari nama: huruf depan tiap kata ("Plaza Central City" → PCC),
# atau 3 huruf pertama kalau satu kata ("Tongbao" → TON).
suggest_code() {
  local words c
  words="$(printf '%s' "$1" | tr -cs '[:alnum:]' ' ' | tr '[:lower:]' '[:upper:]')"
  set -- $words
  if [ $# -ge 2 ]; then c=""; for w in "$@"; do c="$c${w:0:1}"; done; else c="${1:0:3}"; fi
  printf '%s' "${c:0:10}"
}

while [ -z "$NAME" ]; do
  read -rp "Nama lokasi${cur_name:+ [$cur_name]}: " NAME || die "tidak ada input"
  NAME="${NAME:-$cur_name}"
done
while :; do
  if [ -z "$CODE" ]; then
    sug="${cur_code:-$(suggest_code "$NAME")}"
    read -rp "Kode lokasi, 2–10 huruf/angka [$sug]: " CODE || die "tidak ada input"
    CODE="${CODE:-$sug}"
  fi
  CODE="$(printf '%s' "$CODE" | tr '[:lower:]' '[:upper:]')"
  [[ "$CODE" =~ ^[A-Z0-9]{2,10}$ ]] && break
  echo "Kode harus 2–10 huruf/angka tanpa spasi, contoh PCC."; CODE=""
  [ -t 0 ] || die "kode lokasi tidak valid"
done
if [ -n "$cur_code" ] && [ "$CODE" != "$cur_code" ]; then
  echo "Perhatian: kode lokasi berubah dari $cur_code ke $CODE. Kode error baru akan memakai $CODE."
fi
info "Lokasi: $CODE · $NAME"
cfg_set code "$CODE"
cfg_set name "$NAME"
cfg_set enroll_key ""     # kunci diberikan lewat site.key, bukan ditulis di config

sync_ip 1 "$PORT"

if [ -z "$(cfg_get session_secret)" ]; then
  cfg_set session_secret "$(openssl rand -hex 32)"
  info "session_secret dibuat (login dashboard tidak hilang saat restart)"
fi

# --- Kunci lokasi -----------------------------------------------------------

if [ ! -s site.key ]; then
  (umask 077; openssl rand -hex 16 > site.key)
  info "site.key dibuat"
fi
chmod 600 site.key

# --- Akun dashboard ---------------------------------------------------------
# Dua akun standar di setiap lokasi:
#   admin   / Admin@1234  (role admin)
#   teknisi / teknisi123  (role viewer: hanya melihat)
# Bisa diganti lewat SENTINEL_ADMIN_PASSWORD / SENTINEL_TEKNISI_PASSWORD.
# Akun yang sudah ada tidak diubah.

has_user() { ./sentinel-agent user list -users users.yaml 2>/dev/null | awk '{print $1}' | grep -qx "$1"; }

ensure_user() { # ensure_user nama role ENV_VAR password_standar
  local name="$1" role="$2" pw="${!3:-$4}"
  if has_user "$name"; then info "Akun $name sudah ada, tidak diubah"; return; fi
  [ ${#pw} -ge 10 ] || die "password akun $name kurang dari 10 karakter"
  printf '%s\n' "$pw" | ./sentinel-agent user add -users users.yaml -name "$name" -role "$role" >/dev/null 2>&1 \
    || die "gagal membuat akun $name"
  info "Akun $name ($role) dibuat"
}

ensure_user admin   admin  SENTINEL_ADMIN_PASSWORD   'Admin@1234'
ensure_user teknisi viewer SENTINEL_TEKNISI_PASSWORD 'teknisi123'
chmod 600 users.yaml

# --- Service ----------------------------------------------------------------

cmp -s "$SELF" sentinel-sync-ip.sh || { cp "$SELF" sentinel-sync-ip.sh.new && chmod 755 sentinel-sync-ip.sh.new && mv -f sentinel-sync-ip.sh.new sentinel-sync-ip.sh; }

if [ "$NO_SERVICE" = 1 ]; then
  echo; echo "Selesai tanpa service (--no-service). Jalankan manual:"
  echo "  cd $DIR && SENTINEL_SITE_KEY=\"\$(cat site.key)\" ./sentinel-agent -config config.yaml"
  exit 0
fi

mkdir -p "$(dirname "$UNIT")"
cat > "$UNIT" <<EOF
[Unit]
Description=Parkways Sentinel ($CODE)
After=network-online.target

[Service]
WorkingDirectory=$DIR
# Samakan IP di config.yaml dengan IP Tailscale PC ini (tunggu Tailscale siap).
ExecStartPre=$DIR/sentinel-sync-ip.sh --sync-ip -d $DIR
ExecStart=/bin/sh -c 'SENTINEL_SITE_KEY="\$\$(cat site.key)" exec ./sentinel-agent -config config.yaml'
Restart=always
RestartSec=5
Nice=10

[Install]
WantedBy=default.target
EOF
cat > "$(dirname "$UNIT")/sentinel-ipwatch.service" <<EOF
[Unit]
Description=Parkways Sentinel ($CODE): ikuti perubahan IP Tailscale
After=sentinel.service

[Service]
ExecStart=$DIR/sentinel-sync-ip.sh --watch-ip -d $DIR
Restart=always
RestartSec=30
Nice=15

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable sentinel.service sentinel-ipwatch.service >/dev/null
systemctl --user restart sentinel.service sentinel-ipwatch.service
loginctl enable-linger "$USER" 2>/dev/null || echo "catatan: jalankan 'sudo loginctl enable-linger $USER' supaya Sentinel tetap jalan tanpa login"

# --- Cek hasil --------------------------------------------------------------

info "Menunggu agent menyala…"
ok=0
for _ in $(seq 1 15); do
  if curl -fsS -o /dev/null "http://$TS_IP:$PORT/v1/health" 2>/dev/null; then ok=1; break; fi
  sleep 1
done
echo
if [ "$ok" = 1 ]; then
  echo "BERHASIL. Sentinel $CODE ($NAME) berjalan."
else
  echo "Agent belum menjawab. Cek log: journalctl --user -u sentinel -n 50"
fi
# Nama MagicDNS PC ini tidak ikut berubah saat IP Tailscale berubah. Hanya
# ditawarkan kalau nama itu benar-benar mengarah ke IP PC ini.
TS_NAME="$(tailscale status --peers=false --json 2>/dev/null | sed -n 's/.*"DNSName": *"\([^"]*[^".]\)\.\{0,1\}".*/\1/p' | head -n1 || true)"
NAME_URL=""
if [ -n "$TS_NAME" ] && [ "$(getent hosts "$TS_NAME" 2>/dev/null | awk '{print $1; exit}')" = "$TS_IP" ]; then
  NAME_URL="
                atau http://$TS_NAME:$PORT
                (tetap sama walau IP Tailscale berubah; pakai kalau bisa dibuka dari kiosk)"
fi
cat <<EOF

  Dashboard   : http://$TS_IP:$PORT
  Login       : admin / Admin@1234   ·   teknisi / teknisi123 (hanya melihat)
  Agent URL   : http://$TS_IP:$PORT   (diisi di Pengaturan Sentinel setiap gate)$NAME_URL
  Kunci lokasi: $(cat "$DIR/site.key")
                (diisi di setiap gate; jangan kirim lewat grup chat.
                 Lihat lagi kapan saja: cat $DIR/site.key)
  Log         : journalctl --user -u sentinel -f
EOF
exit 0
