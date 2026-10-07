// Gate detail: status, stat tiles, hourly chart, 24h code summary and the
// event timeline. Refreshes every 30s in place; the timeline is not
// reloaded under the user's feet — a "new events" notice appears instead.

import { h, setText } from "../core/dom.js";
import { api, ApiError } from "../core/api.js";
import { Poller } from "../core/poller.js";
import { nf, fmtTime, fmtDuration, STATUS_TEXT, statusLabel } from "../core/format.js";
import { agoEl, setAgo } from "../core/ticker.js";
import { statusBadge, sevBadge } from "../ui/badges.js";
import { tile } from "../ui/tile.js";
import { hourlyChart } from "../ui/chart.js";
import { eventList } from "../ui/events.js";

const INTERVAL = 30000;

export function mount(root, params, ctx) {
  const id = params.id;
  const title = h("h1", { tabindex: "-1" }, h("span", { class: "skeleton-text", text: "STI · G00" }));
  const badge = statusBadge("green", "");
  badge.hidden = true;
  const sub = h("div", { class: "muted small mono", text: id });

  const tErr1 = tile("Error 1 jam");
  const tErr24 = tile("Error 24 jam");
  const tOutbox = tile("Antrean di kiosk", { sub: "event belum terkirim" });
  const tUptime = tile("Uptime aplikasi");

  const info = {};
  const infoItem = (key, label) => {
    info[key] = h("dd", { class: "skeleton-text", text: "—————" });
    return h("div", {}, h("dt", { text: label }), info[key]);
  };
  const seenAgo = agoEl("");
  const seenAbs = h("span");

  const chart = hourlyChart();
  const summaryBox = h("div", { class: "panel table-wrap" }, h("p", { class: "muted", text: "Memuat…" }));
  const events = eventList({ fixed: { device_id: id }, onAuthError: ctx.onAuthError });

  const body = h("div", {},
    h("div", { class: "tiles" }, tErr1, tErr24, tOutbox, tUptime),
    h("div", { class: "panel" }, h("dl", { class: "dl" },
      infoItem("status", "Status"),
      h("div", {}, h("dt", { text: "Terakhir terlihat" }), h("dd", {}, seenAgo, " ", seenAbs)),
      infoItem("hb", "Heartbeat terakhir"),
      infoItem("ev", "Event terakhir"),
      infoItem("app", "Versi aplikasi"),
      infoItem("sdk", "Versi SDK"),
      infoItem("addr", "Alamat"),
      infoItem("pos", "POS"))),
    h("h2", { text: "Error per jam (24 jam terakhir)" }),
    h("div", { class: "panel" }, chart),
    h("h2", { text: "Ringkasan kode error (24 jam)" }),
    summaryBox,
    h("h2", { text: "Timeline event" }),
    events.el);

  root.replaceChildren(
    h("div", { class: "pagehead" }, h("div", {},
      h("a", { href: "#/", class: "small back", text: "← Ringkasan" }),
      h("div", { class: "title-row" }, title, badge), sub)),
    body);

  let summaryKey = "";
  function apply(data) {
    if (data.notFound) {
      poller.stop();
      title.replaceChildren("Gate tidak ditemukan");
      body.replaceChildren(h("div", { class: "panel" },
        h("p", { text: `Device ${id} tidak terdaftar di agent ini.` }),
        h("a", { href: "#/", text: "Kembali ke ringkasan" })));
      return;
    }
    const d = data.device;
    document.title = `${d.location} ${d.gate} — Parkways Sentinel`;
    title.replaceChildren(`${d.location} · ${d.gate}`);
    badge.hidden = false;
    badge.update(d.color, statusLabel(d));
    tErr1.update(nf.format(d.errors_1h || 0));
    tErr24.update(nf.format(d.errors_24h || 0));
    tOutbox.update(nf.format(d.outbox_pending || 0), d.outbox_dropped ? nf.format(d.outbox_dropped) + " event dibuang SDK" : "event belum terkirim");
    tUptime.update(fmtDuration(d.uptime_s));

    const set = (k, v) => { info[k].classList.remove("skeleton-text"); setText(info[k], v); };
    set("status", STATUS_TEXT[d.status] || d.status);
    setAgo(seenAgo, d.last_seen);
    setText(seenAbs, d.last_seen ? "(" + fmtTime(d.last_seen) + ")" : "");
    set("hb", fmtTime(d.last_heartbeat_at));
    set("ev", fmtTime(d.last_event_at));
    set("app", d.app_version || "—");
    set("sdk", d.sdk_version || "—");
    set("addr", d.remote_addr || "—");
    set("pos", d.pos || "—");

    chart.update(data.hourly_24h || []);
    const k = JSON.stringify(data.summary_24h || []);
    if (k !== summaryKey) {
      summaryKey = k;
      summaryBox.replaceChildren(summaryTable(data.summary_24h || [], d.device_id));
    }
    events.noticeNewer(d.last_event_at);
  }

  const poller = new Poller(async (signal) => {
    try {
      return await api("/v1/devices/" + encodeURIComponent(id), { signal });
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return { notFound: true };
      throw err;
    }
  }, { interval: INTERVAL, onData: apply, onAuthError: ctx.onAuthError }).start();

  return { poller, destroy: () => { poller.stop(); events.destroy(); } };
}

function summaryTable(rows, deviceId) {
  if (!rows.length) return h("p", { class: "muted", text: "Tidak ada event dalam 24 jam terakhir." });
  return h("table", {},
    h("thead", {}, h("tr", {}, ["Kode", "Event", "Severity", "Jumlah", "Terakhir"].map((t, i) => h("th", { scope: "col", class: i === 3 ? "num" : null, text: t })))),
    h("tbody", {}, rows.map((r) => h("tr", {},
      h("td", {}, h("a", { class: "mono", href: "#/events?device_id=" + encodeURIComponent(deviceId) + "&error_code=" + encodeURIComponent(r.error_code), text: r.error_code })),
      h("td", { text: r.event }),
      h("td", {}, sevBadge(r.severity)),
      h("td", { class: "num", text: nf.format(r.count) }),
      h("td", {}, agoEl(r.last_seen))))));
}
