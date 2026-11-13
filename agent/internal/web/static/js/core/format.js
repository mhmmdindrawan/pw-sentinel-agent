// Formatting in Indonesian, browser local time.

export const nf = new Intl.NumberFormat("id-ID");
const dtf = new Intl.DateTimeFormat("id-ID", { dateStyle: "medium", timeStyle: "medium" });
export const tf = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit" });

export function fmtTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d) ? iso : dtf.format(d);
}

const dayFmt = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short" });
const timeSecFmt = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit", second: "2-digit" });

// fmtShort: "15.14.03" for today, "6 Okt 23.14" otherwise — compact enough
// for a table cell or a phone card; the full timestamp goes in a tooltip.
export function fmtShort(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  if (d.toDateString() === new Date().toDateString()) return timeSecFmt.format(d);
  return dayFmt.format(d) + " " + tf.format(d);
}

export function fmtClock(date) {
  return tf.format(date);
}

export function fmtAgo(iso) {
  if (!iso) return "belum pernah";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 5) return "baru saja";
  if (s < 60) return s + " dtk lalu";
  if (s < 3600) return Math.floor(s / 60) + " mnt lalu";
  if (s < 86400) return Math.floor(s / 3600) + " jam lalu";
  return Math.floor(s / 86400) + " hari lalu";
}

export function fmtDuration(sec) {
  if (!sec) return "—";
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? d + "h " + hh + "j" : hh ? hh + "j " + m + "m" : m + "m";
}

export const STATUS_TEXT = { ONLINE: "Online", STALE: "Terlambat", DOWN: "Mati", NEVER: "Belum pernah lapor" };
export const COLOR_TEXT = { red: "Bermasalah", yellow: "Perhatian", green: "Normal" };
export const PLATFORM_TEXT = { tongbao: "Tongbao", windows: "Windows", linux: "Linux" };

// statusLabel is what a gate's badge says: liveness when it isn't simply
// online, otherwise the health colour.
export function statusLabel(d) {
  return d.status === "ONLINE" ? COLOR_TEXT[d.color] : STATUS_TEXT[d.status] || d.status;
}

// fmtSinceDuration: how long a state has lasted, e.g. "12 mnt", "3 jam 5 mnt".
export function fmtSinceDuration(iso) {
  if (!iso) return "";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return s + " dtk";
  if (s < 3600) return Math.floor(s / 60) + " mnt";
  if (s < 86400) return Math.floor(s / 3600) + " jam " + Math.floor((s % 3600) / 60) + " mnt";
  return Math.floor(s / 86400) + " hari " + Math.floor((s % 86400) / 3600) + " jam";
}

export function fmtBytes(n) {
  if (!n && n !== 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + " " + units[i];
}

// fmtVersion: "v3.38.0" whether the source said "3.38.0" or "v3.38.0"
// (release tags and some kiosk builds already carry the prefix).
export function fmtVersion(v) {
  if (!v) return "";
  return /^v\d/i.test(v) ? v : "v" + v;
}

export function platformLabel(p) {
  return p ? PLATFORM_TEXT[p] || p : "";
}

// Human names for the registry's event names. The table leads with these;
// the symbolic name stays visible in small print for developers and for
// matching against the kiosk's source.
export const EVENT_LABEL = {
  APP_STARTED: "Aplikasi dimulai", APP_CRASHED: "Aplikasi crash", APP_EXCEPTION: "Kesalahan kode (exception)",
  PAYMENT_REJECTED: "Pembayaran ditolak", PAYMENT_TIMEOUT: "Pembayaran timeout", INSUFFICIENT_BALANCE: "Saldo tidak cukup",
  QRIS_NOT_CONFIGURED: "QRIS belum dikonfigurasi",
  READER_DISCONNECTED: "Pembaca kartu terputus", CARD_READ_TIMEOUT: "Baca kartu timeout",
  READER_NOT_READY: "Pembaca kartu tidak siap", READER_INIT_FAILED: "Pembaca kartu gagal inisialisasi",
  PRINTER_DISCONNECTED: "Printer terputus", PRINT_FAILED: "Cetak gagal",
  LPR_TIMEOUT: "Kamera LPR timeout", LPR_NO_FEED: "Kamera LPR tidak mengirim gambar", CCTV_DISCONNECTED: "CCTV terputus",
  MQTT_DISCONNECTED: "MQTT terputus", MQTT_CONNECT_FAILED: "MQTT gagal terhubung", MQTT_DISABLED: "MQTT dimatikan",
  NETWORK_DISCONNECTED: "Jaringan terputus", HIGH_LATENCY: "Jaringan lambat",
  HTTP_SERVER_ERROR: "Server pembayaran error", HTTP_TIMEOUT: "Server pembayaran timeout", HTTP_CLIENT_ERROR: "Permintaan ke server ditolak",
  DATABASE_ERROR: "Database error", DEVICE_ERROR: "Perangkat error",
  GATE_OPEN_FAILED: "Palang tidak terbuka", PCLESS_DISCONNECTED: "Palang (PCLess) terputus",
  SYSTEM_ERROR: "Sistem error", SENTINEL_GAP: "Kiosk sempat tidak terhubung ke Sentinel",
};

// eventLabel: "Pembaca kartu tidak siap"; catch-all log events
// (LOG_<LOGGER>__<FUNC>) read as "Log aplikasi: cashless.server / request".
export function eventLabel(name) {
  if (!name) return "";
  if (EVENT_LABEL[name]) return EVENT_LABEL[name];
  if (name.startsWith("LOG_")) {
    const [logger, fn] = name.slice(4).split("__");
    const src = (logger || "").toLowerCase().replace(/_/g, ".");
    return "Log aplikasi: " + (src || "root") + (fn ? " / " + fn.toLowerCase() : "");
  }
  return name;
}

// shortCode: "APP-010" from "DEV-G01-APP-010" — the gate is already in its
// own column, so the code only needs to say what, not where.
export function shortCode(code) {
  if (!code) return "";
  const parts = code.split("-");
  return parts.length >= 4 ? parts.slice(-2).join("-") : code;
}

