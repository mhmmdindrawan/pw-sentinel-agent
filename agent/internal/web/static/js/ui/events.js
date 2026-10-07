// Filterable, paginated event table shared by the Events page and the gate
// detail page.
//
// - Every filter change cancels the previous request; a late response for an
//   old filter can never overwrite a newer one (api.latest).
// - Text inputs are debounced; filters can be mirrored into the URL hash so
//   a view is shareable and Back restores it.
// - Rows expand through a real <button aria-expanded>, so keyboard works.
// - A failed load keeps the rows already shown and offers a retry.

import { h, setText, debounce } from "../core/dom.js";
import { api, latest, isAbort, AuthError } from "../core/api.js";
import { connection } from "../core/connection.js";
import { fmtTime, fmtShort } from "../core/format.js";
import { agoEl } from "../core/ticker.js";
import { sevBadge } from "./badges.js";

export const RANGES = [["1h", "1 jam", 3600], ["24h", "24 jam", 86400], ["7d", "7 hari", 7 * 86400], ["30d", "30 hari", 30 * 86400]];
const FILTER_KEYS = ["range", "severity", "location", "device_id", "error_code"];
const DEFAULTS = { range: "24h", severity: "", location: "", device_id: "", error_code: "" };
const PAGE = 100;

export function eventList({ fixed = {}, showDevice = false, params = {}, syncUrl = false, onAuthError }) {
  const f = Object.assign({}, DEFAULTS, pickFilters(params), fixed);
  let oldestId = 0;
  let newestAt = "";
  let rowCount = 0;

  const tbody = h("tbody");
  const cols = showDevice ? 7 : 6;
  const statusLine = h("div", { class: "list-status muted small", role: "status" });
  const retryBtn = h("button", { type: "button", class: "link", text: "Coba lagi", hidden: true, onclick: () => load(retryReset) });
  const moreBtn = h("button", { type: "button", class: "btn", text: "Muat lebih banyak", hidden: true, onclick: () => load(false) });
  const newer = h("div", { class: "newer", hidden: true },
    h("span", { text: "Ada event baru." }), " ",
    h("button", { type: "button", class: "link", text: "Tampilkan", onclick: () => load(true) }));
  let retryReset = true;

  const fetchPage = latest((signal, reset) => {
    const p = new URLSearchParams();
    for (const k of ["device_id", "location", "error_code", "severity"]) if (f[k]) p.set(k, f[k]);
    const sec = (RANGES.find((r) => r[0] === f.range) || RANGES[1])[2];
    p.set("since", new Date(Date.now() - sec * 1000).toISOString().replace(/\.\d+Z$/, "Z"));
    p.set("limit", String(PAGE));
    if (!reset && oldestId) p.set("before_id", oldestId);
    return api("/v1/events?" + p, { signal });
  });

  async function load(reset) {
    retryReset = reset;
    retryBtn.hidden = true;
    moreBtn.disabled = true;
    setText(moreBtn, "Memuat…");
    if (reset) {
      newer.hidden = true;
      tbody.classList.add("loading");
      if (!rowCount) tbody.replaceChildren(...skeletonRows(cols));
    }
    try {
      const evs = await fetchPage(reset);
      if (reset) {
        tbody.replaceChildren();
        rowCount = 0;
        newestAt = evs.length ? evs[0].received_at : "";
      }
      for (const e of evs) tbody.append(...eventRows(e, showDevice, cols));
      rowCount += evs.length;
      if (evs.length) oldestId = evs[evs.length - 1].id;
      moreBtn.hidden = evs.length < PAGE;
      setText(statusLine, rowCount ? "" : "Tidak ada event untuk filter ini.");
    } catch (err) {
      if (isAbort(err)) return;
      if (err instanceof AuthError) return onAuthError(err);
      connection.fail(err);
      if (reset && !rowCount) tbody.replaceChildren();
      setText(statusLine, "Gagal memuat event (" + err.message + ").");
      retryBtn.hidden = false;
    } finally {
      tbody.classList.remove("loading");
      moreBtn.disabled = false;
      setText(moreBtn, "Muat lebih banyak");
    }
  }

  const changed = () => {
    if (syncUrl) {
      const qs = new URLSearchParams();
      for (const k of FILTER_KEYS) if (f[k] && f[k] !== DEFAULTS[k] && !(k in fixed)) qs.set(k, f[k]);
      const s = qs.toString();
      history.replaceState(null, "", "#/events" + (s ? "?" + s : ""));
    }
    load(true);
  };
  const select = (key, label, options) => {
    const el = h("select", { "aria-label": label }, options.map(([v, t]) => h("option", { value: v, text: t, selected: v === f[key] })));
    el.addEventListener("change", () => { f[key] = el.value; changed(); });
    return el;
  };
  const text = (key, label, placeholder) => {
    const el = h("input", { type: "search", placeholder, value: f[key], "aria-label": label, autocomplete: "off", spellcheck: "false" });
    el.addEventListener("input", debounce(() => {
      const v = el.value.trim();
      if (v !== f[key]) { f[key] = v; changed(); }
    }, 300));
    return el;
  };

  const controls = h("div", { class: "controls" },
    select("range", "Rentang waktu", RANGES.map((r) => [r[0], r[1]])),
    select("severity", "Severity minimum", [["", "Semua severity"], ["LOW", "LOW ke atas"], ["MEDIUM", "MEDIUM ke atas"], ["HIGH", "HIGH ke atas"], ["CRITICAL", "CRITICAL"]]),
    "location" in fixed || "device_id" in fixed ? null : text("location", "Lokasi", "Lokasi (mis. STI)"),
    "device_id" in fixed ? null : text("device_id", "Device ID", "Device ID"),
    text("error_code", "Kode error", "Kode error (mis. STI-G03-PAY-002)"));

  const head = ["", "Waktu", showDevice ? "Gate" : null, "Kode", "Event", "Severity", "Pesan"].filter((x) => x !== null);
  const el = h("div", { class: "event-list" },
    controls, newer,
    h("div", { class: "panel table-wrap" },
      h("table", { class: "events" },
        h("thead", {}, h("tr", {}, head.map((t, i) => h("th", { text: t, scope: "col", "aria-label": i === 0 ? "Detail" : null })))),
        tbody),
      h("div", { class: "list-foot" }, statusLine, retryBtn, moreBtn)));

  load(true);
  return {
    el,
    reload: () => load(true),
    // noticeNewer shows the "new events" pill when the device has reported
    // something newer than the top row — without disturbing open rows.
    noticeNewer(lastEventAt) {
      if (lastEventAt && newestAt && lastEventAt > newestAt) newer.hidden = false;
    },
    destroy: () => fetchPage.cancel(),
  };
}

function pickFilters(params) {
  const out = {};
  for (const k of FILTER_KEYS) if (params[k]) out[k] = params[k];
  return out;
}

function skeletonRows(cols) {
  return Array.from({ length: 4 }, () => h("tr", { class: "skeleton-row", "aria-hidden": "true" },
    Array.from({ length: cols }, () => h("td", {}, h("span", { class: "skeleton-text", text: "xxxxxxxx" })))));
}

function eventRows(e, showDevice, cols) {
  const detailId = "ev-" + e.id;
  const detail = h("tr", { class: "detail", id: detailId, hidden: true },
    h("td", { colspan: cols }, h("pre", { class: "ctx", text: JSON.stringify(eventDetail(e), null, 2) })));
  const toggle = h("button", { type: "button", class: "expand", "aria-expanded": "false", "aria-controls": detailId,
    "aria-label": "Detail event " + e.event });
  toggle.append(h("span", { "aria-hidden": "true", text: "▸" }));
  const flip = () => {
    const open = detail.hidden;
    detail.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
    toggle.classList.toggle("open", open);
  };
  toggle.addEventListener("click", (ev) => { ev.stopPropagation(); flip(); });

  const coalesced = e.context && e.context.coalesced_count;
  const when = e.occurred_at || e.received_at;
  // Cell classes double as grid areas for the phone card layout (app.css).
  const row = h("tr", { class: "clickable" },
    h("td", { class: "c-toggle" }, toggle),
    h("td", { class: "c-time" }, h("span", { title: fmtTime(when), text: fmtShort(when) }), " ", agoEl(e.received_at, "muted small ago")),
    showDevice ? h("td", { class: "c-gate" }, h("a", { href: "#/device/" + encodeURIComponent(e.device_id), text: e.location + " " + e.gate })) : null,
    h("td", { class: "c-code mono", text: e.error_code }),
    h("td", { class: "c-event" }, e.event, coalesced ? h("span", { class: "muted small", text: " ×" + (coalesced + 1) }) : null),
    h("td", { class: "c-sev" }, sevBadge(e.severity)),
    h("td", { class: "c-msg msg", text: e.message || "" }));
  // Mouse convenience: clicking the row toggles too, except on links/buttons.
  row.addEventListener("click", (ev) => {
    if (!ev.target.closest("a, button")) flip();
  });
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
