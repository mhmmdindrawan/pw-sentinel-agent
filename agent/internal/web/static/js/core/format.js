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

// statusLabel is what a gate's badge says: liveness when it isn't simply
// online, otherwise the health colour.
export function statusLabel(d) {
  return d.status === "ONLINE" ? COLOR_TEXT[d.color] : STATUS_TEXT[d.status] || d.status;
}
