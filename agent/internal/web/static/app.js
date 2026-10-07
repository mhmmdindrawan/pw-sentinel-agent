/* Parkways Sentinel dashboard — vanilla JS, no build step, no CDN.
 *
 * Security: event messages and context come from kiosks and are untrusted.
 * Everything is rendered with textContent via h(); never innerHTML. */
"use strict";

const REFRESH_MS = 15000;
const app = document.getElementById("app");
let refreshTimer = null;

// ---------------------------------------------------------------- helpers

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "text") el.textContent = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function svg(tag, attrs, ...children) {
  const el = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "text") el.textContent = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  children.flat().forEach((c) => c && el.append(c));
  return el;
}

class AuthError extends Error {}

async function api(path, opts) {
  const res = await fetch(path, Object.assign({ credentials: "same-origin", headers: { "Content-Type": "application/json" } }, opts));
  if (res.status === 401) throw new AuthError("login required");
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || "HTTP " + res.status);
  return body;
}

const nf = new Intl.NumberFormat("id-ID");
const dtf = new Intl.DateTimeFormat("id-ID", { dateStyle: "medium", timeStyle: "medium" });
const tf = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit" });

function fmtTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d) ? iso : dtf.format(d);
}

function fmtAgo(iso) {
  if (!iso) return "belum pernah";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return "baru saja";
  if (s < 60) return s + " dtk lalu";
  if (s < 3600) return Math.floor(s / 60) + " mnt lalu";
  if (s < 86400) return Math.floor(s / 3600) + " jam lalu";
  return Math.floor(s / 86400) + " hari lalu";
}

function fmtDuration(sec) {
  if (!sec) return "—";
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? d + "h " + hh + "j" : hh ? hh + "j " + m + "m" : m + "m";
}

const STATUS_TEXT = { ONLINE: "Online", STALE: "Terlambat", DOWN: "Mati", NEVER: "Belum pernah lapor" };
const COLOR_TEXT = { red: "Bermasalah", yellow: "Perhatian", green: "Normal" };

function statusBadge(color, label) {
  return h("span", { class: "status " + color }, h("span", { class: "ico", "aria-hidden": "true" }), label);
}

function sevBadge(sev) {
  return h("span", { class: "sev " + (sev || "") , text: sev || "—" });
}

function tile(label, value, sub, extraLabel) {
  return h("div", { class: "tile" },
    h("div", { class: "label" }, extraLabel, label),
    h("div", { class: "value", text: value }),
    sub ? h("div", { class: "sub", text: sub }) : null);
}

function setPage(...nodes) {
  app.replaceChildren(...nodes.flat().filter((n) => n != null && n !== false));
}

function stopRefresh() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = null;
}

function showError(err) {
  if (err instanceof AuthError) return renderLogin();
  setPage(h("div", { class: "notice error-box", text: "Gagal memuat data: " + err.message }));
}

// ---------------------------------------------------------------- login

function renderLogin() {
  stopRefresh();
  document.getElementById("topbar").hidden = true;
  const msg = h("div", { class: "notice error-box", hidden: true });
  const user = h("input", { name: "username", autocomplete: "username", required: true });
  const pass = h("input", { name: "password", type: "password", autocomplete: "current-password", required: true });
  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      msg.hidden = true;
      try {
        await api("/v1/auth/login", { method: "POST", body: JSON.stringify({ username: user.value, password: pass.value }) });
        route();
      } catch (err) {
        msg.textContent = err.message === "invalid username or password" ? "Username atau password salah." : err.message;
        msg.hidden = false;
      }
    },
  },
  h("label", {}, "Username", user),
  h("label", {}, "Password", pass),
  h("button", { class: "btn primary", type: "submit", text: "Masuk" }));
  setPage(h("div", { class: "login panel" }, h("h1", { text: "Parkways Sentinel" }),
    h("p", { class: "muted", text: "Masuk untuk melihat status gate." }), msg, form));
  user.focus();
}

document.getElementById("logout").addEventListener("click", async () => {
  await fetch("/v1/auth/logout", { method: "POST", credentials: "same-origin" });
  renderLogin();
});

// ---------------------------------------------------------------- fleet

const fleetFilter = { location: "", q: "" };

async function renderFleet() {
  const data = await api("/v1/fleet");
  const devices = data.devices || [];
  const count = (c) => devices.filter((d) => d.color === c).length;

  const locations = [...new Set(devices.map((d) => d.location))].sort();
  const q = fleetFilter.q.trim().toLowerCase();
  const shown = devices.filter((d) =>
    (!fleetFilter.location || d.location === fleetFilter.location) &&
    (!q || [d.device_id, d.gate, d.location, d.last_error && d.last_error.event].join(" ").toLowerCase().includes(q)));

  const locSelect = h("select", {
    "aria-label": "Lokasi",
    onchange: (e) => { fleetFilter.location = e.target.value; renderFleet().catch(showError); },
  }, h("option", { value: "", text: "Semua lokasi" }), locations.map((l) => h("option", { value: l, text: l, selected: l === fleetFilter.location })));
  const search = h("input", {
    type: "search", placeholder: "Cari gate / device / event", value: fleetFilter.q, "aria-label": "Cari",
    oninput: (e) => { fleetFilter.q = e.target.value; clearTimeout(search._t); search._t = setTimeout(() => renderFleet().then(() => {
      const s = app.querySelector("input[type=search]"); s.focus(); s.setSelectionRange(s.value.length, s.value.length);
    }).catch(showError), 250); },
  });

  const sections = [];
  for (const loc of locations) {
    const list = shown.filter((d) => d.location === loc);
    if (!list.length) continue;
    const bad = list.filter((d) => d.color === "red").length;
    sections.push(h("section", { class: "loc" },
      h("h2", {}, loc, h("span", { class: "muted small", text: list.length + " gate" + (bad ? " · " + bad + " bermasalah" : "") })),
      h("div", { class: "grid" }, list.map(gateCard))));
  }

  setPage(
    h("div", { class: "pagehead" },
      h("div", {}, h("h1", { text: "Ringkasan gate" }),
        h("div", { class: "muted small", text: "Diperbarui " + tf.format(new Date()) + " · otomatis tiap 15 detik" }))),
    h("div", { class: "tiles" },
      tile("Total gate", nf.format(devices.length)),
      tile("Bermasalah", nf.format(count("red")), "mati, belum lapor, atau error berat", statusBadge("red", "")),
      tile("Perhatian", nf.format(count("yellow")), "terlambat, error sedang, atau antrean", statusBadge("yellow", "")),
      tile("Normal", nf.format(count("green")), null, statusBadge("green", ""))),
    data.unmapped_24h ? h("div", { class: "notice" },
      nf.format(data.unmapped_24h) + " event 24 jam terakhir datang dari kiosk dengan device_id yang tidak cocok dengan tokennya (UNMAPPED). ",
      h("a", { href: "#/events?location=UNMAPPED", text: "Lihat event" })) : null,
    h("div", { class: "controls" }, locSelect, search),
    sections.length ? sections : h("p", { class: "muted", text: devices.length ? "Tidak ada gate yang cocok dengan filter." : "Belum ada device di devices.yaml." }));
}

function gateCard(d) {
  const label = d.status === "ONLINE" ? COLOR_TEXT[d.color] : STATUS_TEXT[d.status];
  const le = d.last_error;
  return h("a", { class: "card " + d.color, href: "#/device/" + encodeURIComponent(d.device_id) },
    h("div", { class: "row" }, h("span", { class: "gate", text: d.gate }), statusBadge(d.color, label)),
    h("div", { class: "meta", text: d.device_id + (d.app_version ? " · v" + d.app_version : "") }),
    h("div", { class: "meta", text: "Terakhir terlihat " + fmtAgo(d.last_seen) }),
    h("div", { class: "counts" },
      h("span", {}, "Error 1 jam ", h("b", { text: nf.format(d.errors_1h || 0) })),
      h("span", {}, "24 jam ", h("b", { text: nf.format(d.errors_24h || 0) })),
      d.outbox_pending ? h("span", {}, "Antrean ", h("b", { text: nf.format(d.outbox_pending) })) : null),
    le ? h("div", { class: "last" },
      h("div", {}, sevBadge(le.severity), " ", h("span", { class: "ev", text: le.event })),
      h("div", { class: "muted mono", text: le.error_code + " · " + fmtAgo(le.received_at) })) : null);
}

// ---------------------------------------------------------------- device

async function renderDevice(id) {
  const data = await api("/v1/devices/" + encodeURIComponent(id));
  const d = data.device;
  const label = d.status === "ONLINE" ? COLOR_TEXT[d.color] : STATUS_TEXT[d.status];

  const eventsBox = h("div");
  setPage(
    h("div", { class: "pagehead" },
      h("div", {},
        h("a", { href: "#/", class: "small", text: "← Ringkasan" }),
        h("h1", {}, d.location + " · " + d.gate + " ", statusBadge(d.color, label)),
        h("div", { class: "muted small mono", text: d.device_id }))),
    h("div", { class: "tiles" },
      tile("Error 1 jam", nf.format(d.errors_1h || 0)),
      tile("Error 24 jam", nf.format(d.errors_24h || 0)),
      tile("Antrean di kiosk", nf.format(d.outbox_pending || 0), d.outbox_dropped ? nf.format(d.outbox_dropped) + " event dibuang" : "event belum terkirim"),
      tile("Uptime aplikasi", fmtDuration(d.uptime_s))),
    h("div", { class: "panel" }, h("dl", { class: "dl" },
      dlItem("Status", STATUS_TEXT[d.status] || d.status),
      dlItem("Terakhir terlihat", fmtAgo(d.last_seen) + (d.last_seen ? " (" + fmtTime(d.last_seen) + ")" : "")),
      dlItem("Heartbeat terakhir", fmtTime(d.last_heartbeat_at)),
      dlItem("Event terakhir", fmtTime(d.last_event_at)),
      dlItem("Versi aplikasi", d.app_version || "—"),
      dlItem("Versi SDK", d.sdk_version || "—"),
      dlItem("Alamat", d.remote_addr || "—"),
      dlItem("POS", d.pos || "—"))),
    h("h2", { text: "Error per jam (24 jam terakhir)" }),
    h("div", { class: "panel" }, hourlyChart(data.hourly_24h || [])),
    h("h2", { text: "Ringkasan kode error (24 jam)" }),
    h("div", { class: "panel table-wrap" }, summaryTable(data.summary_24h || [], d.device_id)),
    h("h2", { text: "Timeline event" }),
    eventsBox);
  eventList(eventsBox, { device_id: d.device_id }, false);
}

function dlItem(k, v) {
  return h("div", {}, h("dt", { text: k }), h("dd", { text: v }));
}

function summaryTable(rows, deviceId) {
  if (!rows.length) return h("p", { class: "muted", text: "Tidak ada event dalam 24 jam terakhir." });
  return h("table", {},
    h("thead", {}, h("tr", {}, ["Kode", "Event", "Severity", "Jumlah", "Terakhir"].map((t, i) => h("th", { class: i === 3 ? "num" : null, text: t })))),
    h("tbody", {}, rows.map((r) => h("tr", {},
      h("td", {}, h("a", { class: "mono", href: "#/events?device_id=" + encodeURIComponent(deviceId) + "&error_code=" + encodeURIComponent(r.error_code), text: r.error_code })),
      h("td", { text: r.event }),
      h("td", {}, sevBadge(r.severity)),
      h("td", { class: "num", text: nf.format(r.count) }),
      h("td", { text: fmtAgo(r.last_seen) })))));
}

/* Single-series bar chart: one hue, thin bars anchored to the baseline
   with 4px rounded tops, recessive grid, hover tooltip per bar, and a table
   view for screen readers / exact values. Drawn at the container's real
   pixel width (and redrawn on resize) so text is never stretched. */
function hourlyChart(hours) {
  const wrap = h("div", { class: "chart" });
  const tip = h("div", { class: "tooltip", hidden: true });
  let lastW = 0;
  const draw = () => {
    const W = Math.round(wrap.clientWidth);
    if (!W || W === lastW) return;
    lastW = W;
    wrap.replaceChildren(drawBars(hours, W, tip), tip);
  };
  new ResizeObserver(draw).observe(wrap);

  const table = h("details", { class: "small" }, h("summary", { class: "muted", text: "Lihat sebagai tabel" }),
    h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", { text: "Jam" }), h("th", { class: "num", text: "Error" }))),
      h("tbody", {}, hours.map((hr) => h("tr", {}, h("td", { text: fmtTime(hr.hour + ":00:00Z") }), h("td", { class: "num", text: String(hr.count) })))))));
  return h("div", {}, wrap, table);
}

function drawBars(hours, W, tip) {
  const H = 180, padL = 30, padB = 22, padT = 8;
  const max = Math.max(1, ...hours.map((x) => x.count));
  // Round the scale up to an even multiple so the midline tick is a whole
  // number: 5 → 6 (ticks 0/3/6), 15 → 20 (0/10/20), 150 → 200.
  const unit = max <= 10 ? 2 : Math.pow(10, Math.floor(Math.log10(max)));
  const nice = Math.ceil(max / unit) * unit;
  const plotW = W - padL, plotH = H - padB - padT, base = padT + plotH;
  const slot = plotW / Math.max(1, hours.length);
  const barW = Math.max(2, Math.min(28, slot * 0.6));
  const y = (v) => base - (v / nice) * plotH;
  const total = hours.reduce((a, x) => a + x.count, 0);

  const root = svg("svg", { width: W, height: H, viewBox: `0 0 ${W} ${H}`, role: "img",
    "aria-label": `Error per jam, 24 jam terakhir: total ${total}, tertinggi ${max} dalam satu jam.` });
  for (const v of [0, nice / 2, nice]) {
    root.append(svg("line", { class: "grid-line", x1: padL, x2: W, y1: y(v), y2: y(v) }));
    root.append(svg("text", { class: "axis-label", x: padL - 6, y: y(v) + 4, "text-anchor": "end", text: String(Math.round(v)) }));
  }
  hours.forEach((hr, i) => {
    const cx = padL + (i + 0.5) * slot, x = cx - barW / 2;
    const label = tf.format(new Date(hr.hour + ":00:00Z"));
    let bar = null;
    if (hr.count > 0) {
      const top = y(hr.count), r = Math.min(4, barW / 2, base - top);
      bar = svg("path", { class: "bar",
        d: `M${x},${base} V${top + r} Q${x},${top} ${x + r},${top} H${x + barW - r} Q${x + barW},${top} ${x + barW},${top + r} V${base} Z` });
      root.append(bar);
    }
    root.append(svg("rect", {
      class: "hit", x: padL + i * slot, y: padT, width: slot, height: plotH,
      onmouseenter: () => {
        if (bar) bar.classList.add("hover");
        tip.textContent = `${label} · ${hr.count} error`;
        tip.style.left = cx + "px";
        tip.style.top = (y(hr.count) - 6) + "px";
        tip.hidden = false;
      },
      onmouseleave: () => { if (bar) bar.classList.remove("hover"); tip.hidden = true; },
    }));
    if (i % 6 === 0 || i === hours.length - 1) {
      root.append(svg("text", { class: "axis-label", x: cx, y: H - 6, "text-anchor": "middle", text: label }));
    }
  });
  return root;
}

// ---------------------------------------------------------------- events

const RANGES = [["1h", "1 jam", 3600], ["24h", "24 jam", 86400], ["7d", "7 hari", 7 * 86400], ["30d", "30 hari", 30 * 86400]];

/* eventList renders filters + a paginated table into box. fixed holds
   filters that can't be changed here (e.g. device_id on a device page). */
function eventList(box, fixed, showDevice, initial) {
  const f = Object.assign({ range: "24h", severity: "", error_code: "", location: "", device_id: "" }, initial || {}, fixed);
  let beforeId = 0;
  const tbody = h("tbody");
  const more = h("button", { class: "btn", type: "button", text: "Muat lebih banyak", hidden: true, onclick: () => load(false) });
  const status = h("div", { class: "muted small" });

  async function load(reset) {
    if (reset) { beforeId = 0; tbody.replaceChildren(); }
    const p = new URLSearchParams();
    for (const k of ["device_id", "location", "error_code", "severity"]) if (f[k]) p.set(k, f[k]);
    const sec = (RANGES.find((r) => r[0] === f.range) || RANGES[1])[2];
    p.set("since", new Date(Date.now() - sec * 1000).toISOString().replace(/\.\d+Z$/, "Z"));
    p.set("limit", "100");
    if (beforeId) p.set("before_id", beforeId);
    try {
      const evs = await api("/v1/events?" + p);
      evs.forEach((e) => tbody.append(...eventRows(e, showDevice)));
      if (evs.length) beforeId = evs[evs.length - 1].id;
      more.hidden = evs.length < 100;
      status.textContent = tbody.querySelectorAll("tr.clickable").length ? "" : "Tidak ada event untuk filter ini.";
    } catch (err) { showError(err); }
  }

  const ctl = (key, el) => { el.addEventListener("change", () => { f[key] = el.value.trim(); load(true); }); return el; };
  const controls = h("div", { class: "controls" },
    ctl("range", h("select", { "aria-label": "Rentang waktu" }, RANGES.map((r) => h("option", { value: r[0], text: r[1], selected: r[0] === f.range })))),
    ctl("severity", h("select", { "aria-label": "Severity minimum" },
      [["", "Semua severity"], ["LOW", "LOW ke atas"], ["MEDIUM", "MEDIUM ke atas"], ["HIGH", "HIGH ke atas"], ["CRITICAL", "CRITICAL"]]
        .map((o) => h("option", { value: o[0], text: o[1], selected: o[0] === f.severity })))),
    fixed.location !== undefined || fixed.device_id !== undefined ? null : ctl("location", h("input", { type: "search", placeholder: "Lokasi (mis. STI)", value: f.location, "aria-label": "Lokasi" })),
    fixed.device_id !== undefined ? null : ctl("device_id", h("input", { type: "search", placeholder: "Device ID", value: f.device_id, "aria-label": "Device ID" })),
    ctl("error_code", h("input", { type: "search", placeholder: "Kode error (mis. STI-G03-PAY-002)", value: f.error_code, "aria-label": "Kode error" })));

  const head = ["Waktu", showDevice ? "Gate" : null, "Kode", "Event", "Severity", "Pesan"].filter(Boolean);
  box.replaceChildren(controls, h("div", { class: "panel table-wrap" },
    h("table", {}, h("thead", {}, h("tr", {}, head.map((t) => h("th", { text: t, class: t === "Pesan" ? "hide-sm" : null })))), tbody),
    status, h("div", { class: "controls", style: "margin-top:12px" }, more)));
  load(true);
}

function eventRows(e, showDevice) {
  const detail = h("tr", { class: "detail", hidden: true },
    h("td", { colspan: showDevice ? 6 : 5 }, h("pre", { class: "ctx", text: JSON.stringify(eventDetail(e), null, 2) })));
  const row = h("tr", { class: "clickable", onclick: () => { detail.hidden = !detail.hidden; } },
    h("td", { title: fmtTime(e.received_at) }, h("div", { text: fmtTime(e.occurred_at || e.received_at) }), h("div", { class: "muted small", text: fmtAgo(e.received_at) })),
    showDevice ? h("td", {}, h("a", { href: "#/device/" + encodeURIComponent(e.device_id), text: e.location + " " + e.gate, onclick: (ev) => ev.stopPropagation() })) : null,
    h("td", { class: "mono", text: e.error_code }),
    h("td", {}, e.event, e.context && e.context.coalesced_count ? h("span", { class: "muted small", text: " ×" + (e.context.coalesced_count + 1) }) : null),
    h("td", {}, sevBadge(e.severity)),
    h("td", { class: "hide-sm", text: e.message || "" }));
  return [row, detail];
}

function eventDetail(e) {
  const out = {};
  for (const k of ["event_id", "device_id", "device", "occurred_at", "received_at", "message", "endpoint", "http_status",
    "duration_ms", "retry_count", "transaction_ref", "app_version", "context"]) {
    if (e[k] != null && e[k] !== "") out[k] = e[k];
  }
  return out;
}

function renderEvents(params) {
  const box = h("div");
  setPage(h("div", { class: "pagehead" }, h("h1", { text: "Event" })), box);
  eventList(box, {}, true, params);
}

// ---------------------------------------------------------------- router

async function route() {
  stopRefresh();
  const hash = location.hash.slice(1) || "/";
  const [path, query] = hash.split("?");
  const params = Object.fromEntries(new URLSearchParams(query || ""));
  try {
    const me = await api("/v1/auth/me");
    document.getElementById("topbar").hidden = false;
    document.getElementById("who-name").textContent = me.username;
  } catch (err) {
    return showError(err);
  }
  const page = path.startsWith("/device/") ? "fleet" : path === "/events" ? "events" : "fleet";
  document.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("active", a.dataset.nav === page));

  try {
    if (path.startsWith("/device/")) {
      const id = decodeURIComponent(path.slice("/device/".length));
      await renderDevice(id);
    } else if (path === "/events") {
      renderEvents(params);
    } else {
      await renderFleet();
      refreshTimer = setInterval(() => {
        if (document.activeElement && document.activeElement.matches("select")) return;
        renderFleet().catch(showError);
      }, REFRESH_MS);
    }
  } catch (err) {
    showError(err);
  }
}

window.addEventListener("hashchange", route);
route();
