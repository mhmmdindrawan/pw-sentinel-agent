// Ringkasan: every gate grouped by location, red first. Built once; each
// refresh updates the existing nodes in place (keyed by device_id) so
// scroll position, keyboard focus and hover survive the 15-second poll.

import { h, setText, debounce } from "../core/dom.js";
import { api } from "../core/api.js";
import { Poller } from "../core/poller.js";
import { connection } from "../core/connection.js";
import { nf, fmtClock, fmtBytes, statusLabel, platformLabel } from "../core/format.js";
import { agoEl, setAgo, sinceEl, setSince } from "../core/ticker.js";
import { statusBadge, statusIcon, sevBadge } from "../ui/badges.js";
import { tile } from "../ui/tile.js";

const INTERVAL = 15000;
const STATUS_TEXT_SINCE = { STALE: "Terlambat sejak", DOWN: "Mati sejak" };
// Filters survive navigating to a gate and back.
const filter = { location: "", q: "" };

export function mount(root, _params, ctx) {
  const updated = h("span", { class: "updated", text: "Memuat data…" });
  const tTotal = tile("Total gate");
  const tRed = tile("Bermasalah", { icon: statusIcon("red"), sub: "mati, belum lapor, atau error berat" });
  const tYellow = tile("Perhatian", { icon: statusIcon("yellow"), sub: "terlambat, error sedang, atau antrean" });
  const tGreen = tile("Normal", { icon: statusIcon("green") });
  const unmapped = h("div", { class: "notice", hidden: true });
  // Where support is looking: site code/name and how this agent is reached.
  const siteLine = h("div", { class: "site-line muted small", hidden: true });
  const agentNotice = h("div", { class: "notice", hidden: true });

  const locSelect = h("select", { "aria-label": "Filter lokasi" }, h("option", { value: "", text: "Semua lokasi" }));
  locSelect.addEventListener("change", () => { filter.location = locSelect.value; applyFilter(); });
  const search = h("input", { type: "search", placeholder: "Cari gate, device, atau event", "aria-label": "Cari gate", value: filter.q, autocomplete: "off" });
  search.addEventListener("input", debounce(() => { filter.q = search.value.trim().toLowerCase(); applyFilter(); }, 150));

  const sectionsEl = h("div", { class: "sections" }, skeletonCards());
  const empty = h("p", { class: "empty", hidden: true });

  root.replaceChildren(
    h("div", { class: "pagehead" }, h("div", {}, h("h1", { tabindex: "-1", text: "Ringkasan gate" }), siteLine, updated)),
    h("div", { class: "tiles" }, tTotal, tRed, tYellow, tGreen),
    agentNotice,
    unmapped,
    h("div", { class: "controls" }, locSelect, search),
    sectionsEl, empty);

  const cards = new Map(); // device_id -> card element
  const sections = new Map(); // location -> {el, grid, meta}
  let devices = [];
  let first = true;

  function apply(data) {
    devices = data.devices || [];
    if (first) {
      sectionsEl.replaceChildren();
      first = false;
    }
    const count = (c) => devices.filter((d) => d.color === c).length;
    tTotal.update(nf.format(devices.length));
    tRed.update(nf.format(count("red")));
    tYellow.update(nf.format(count("yellow")));
    tGreen.update(nf.format(count("green")));

    renderSite(data.site || {}, data.agent || {});
    if (data.unmapped_24h) {
      unmapped.replaceChildren(
        `${nf.format(data.unmapped_24h)} event dalam 24 jam terakhir datang dari kiosk yang device_id-nya tidak cocok dengan tokennya (UNMAPPED). `,
        h("a", { href: "#/events?location=UNMAPPED", text: "Lihat event" }));
      unmapped.hidden = false;
    } else {
      unmapped.hidden = true;
    }

    const locations = [...new Set(devices.map((d) => d.location))].sort();
    syncOptions(locations);

    // Reconcile sections and cards.
    const seen = new Set();
    let prevSection = null;
    for (const loc of locations) {
      let sec = sections.get(loc);
      if (!sec) {
        const meta = h("span", { class: "muted small" });
        const grid = h("div", { class: "grid" });
        sec = { el: h("section", { class: "loc", "aria-label": "Lokasi " + loc }, h("h2", {}, loc, meta), grid), grid, meta };
        sections.set(loc, sec);
      }
      placeAfter(sectionsEl, sec.el, prevSection);
      prevSection = sec.el;

      const list = devices.filter((d) => d.location === loc);
      let prevCard = null;
      for (const d of list) {
        let card = cards.get(d.device_id);
        if (!card) {
          card = gateCard();
          cards.set(d.device_id, card);
        }
        card.update(d);
        placeAfter(sec.grid, card, prevCard);
        prevCard = card;
        seen.add(d.device_id);
      }
    }
    for (const [id, card] of cards) {
      if (!seen.has(id)) { card.remove(); cards.delete(id); }
    }
    for (const [loc, sec] of sections) {
      if (!locations.includes(loc)) { sec.el.remove(); sections.delete(loc); }
    }
    renderUpdated();
    applyFilter();
  }

  let siteKey = "";
  function renderSite(site, agent) {
    const key = JSON.stringify([site, agent.version, agent.tailscale_ip, agent.listen_addr, agent.db_size_bytes > agent.db_max_bytes * 0.8, agent.disk_free_bytes]);
    if (key === siteKey) return;
    siteKey = key;
    const parts = [];
    if (site.code) parts.push(h("b", { text: site.code + (site.name ? " · " + site.name : "") }));
    if (agent.tailscale_ip) parts.push("agent " + agent.tailscale_ip + (agent.listen_addr ? ":" + agent.listen_addr.split(":").pop() : ""));
    else if (agent.listen_addr) parts.push("agent " + agent.listen_addr);
    if (agent.version) parts.push("v" + agent.version);
    if (site.code && site.enroll_enabled) parts.push("pendaftaran kiosk aktif");
    siteLine.replaceChildren(...parts.flatMap((p, i) => (i ? [" · ", p] : [p])));
    siteLine.hidden = !parts.length;
    document.title = (site.code ? site.code + " — " : "") + "Parkways Sentinel";

    const warn = [];
    if (agent.db_max_bytes && agent.db_size_bytes > agent.db_max_bytes * 0.8) {
      warn.push(`Database agent ${fmtBytes(agent.db_size_bytes)} dari batas ${fmtBytes(agent.db_max_bytes)}. Event tertua akan dibuang otomatis; periksa kiosk yang mengirim event berulang.`);
    }
    if (agent.disk_free_bytes && agent.disk_free_bytes < 1 << 30) {
      warn.push(`Sisa disk server agent hanya ${fmtBytes(agent.disk_free_bytes)}.`);
    }
    agentNotice.replaceChildren(...warn.map((t) => h("div", { text: t })));
    agentNotice.hidden = !warn.length;
  }

  function syncOptions(locations) {
    const have = [...locSelect.options].slice(1).map((o) => o.value);
    if (have.join() === locations.join()) return;
    locSelect.replaceChildren(h("option", { value: "", text: "Semua lokasi" }),
      ...locations.map((l) => h("option", { value: l, text: l })));
    locSelect.value = locations.includes(filter.location) ? filter.location : "";
  }

  function applyFilter() {
    let visibleTotal = 0;
    for (const [loc, sec] of sections) {
      const list = devices.filter((d) => d.location === loc);
      let visible = 0, bad = 0;
      for (const d of list) {
        const show = (!filter.location || d.location === filter.location) && matches(d, filter.q);
        cards.get(d.device_id).hidden = !show;
        if (show) { visible++; if (d.color === "red") bad++; }
      }
      sec.el.hidden = visible === 0;
      setText(sec.meta, `${visible} gate${bad ? " · " + bad + " bermasalah" : ""}`);
      visibleTotal += visible;
    }
    empty.hidden = first || visibleTotal > 0;
    setText(empty, devices.length ? "Tidak ada gate yang cocok dengan filter." : "Belum ada device terdaftar. Tambahkan dengan `sentinel-agent device add`.");
  }

  function renderUpdated() {
    const st = connection.get();
    if (!st.lastOk) return;
    const stale = connection.isStale();
    setText(updated, stale
      ? `Data belum diperbarui sejak ${fmtClock(st.lastOk)}`
      : `Diperbarui ${fmtClock(st.lastOk)} · otomatis tiap 15 detik`);
    updated.classList.toggle("stale", stale);
  }
  const unsub = connection.subscribe(renderUpdated);

  const poller = new Poller((signal) => api("/v1/fleet", { signal }), {
    interval: INTERVAL, onData: apply, onAuthError: ctx.onAuthError,
  }).start();

  return { poller, destroy: () => { poller.stop(); unsub(); } };
}

function matches(d, q) {
  if (!q) return true;
  return [d.device_id, d.gate, d.location, d.name, d.remote_addr, d.last_error && d.last_error.event, d.last_error && d.last_error.error_code]
    .join(" ").toLowerCase().includes(q);
}

// placeAfter moves el right after prev (or to the front) only if it isn't
// already there — moving a node would drop keyboard focus inside it.
function placeAfter(parent, el, prev) {
  const want = prev ? prev.nextSibling : parent.firstChild;
  if (want === el) return;
  parent.insertBefore(el, want);
}

function gateCard() {
  const gate = h("span", { class: "gate" });
  const badge = statusBadge("green", "");
  const name = h("div", { class: "name", hidden: true });
  const ident = h("div", { class: "meta" });
  const since = sinceEl("", "since");
  const seen = agoEl("");
  const errs1 = h("b"), errs24 = h("b"), outbox = h("b");
  const outboxWrap = h("span", {}, "Antrean ", outbox);
  const lastSev = sevBadge("");
  const lastEvent = h("span", { class: "ev" });
  const lastCode = h("span");
  const lastAgo = agoEl("");
  const last = h("div", { class: "last" },
    h("div", {}, lastSev, " ", lastEvent),
    h("div", { class: "muted mono" }, lastCode, " · ", lastAgo));
  const hint = h("div", { class: "hint", text: "Belum pernah lapor — cek SENTINEL_AGENT_URL dan SENTINEL_TOKEN di kiosk." });

  const el = h("a", { class: "card" },
    h("div", { class: "row" }, gate, badge),
    name,
    ident,
    since,
    h("div", { class: "meta" }, "Terakhir terlihat ", seen),
    h("div", { class: "counts" },
      h("span", {}, "Error 1 jam ", errs1), h("span", {}, "24 jam ", errs24), outboxWrap),
    last, hint);

  el.update = (d) => {
    el.setAttribute("href", "#/device/" + encodeURIComponent(d.device_id));
    el.className = "card " + d.color;
    el.setAttribute("aria-label", `${d.location} ${d.gate}: ${statusLabel(d)}`);
    setText(gate, d.gate);
    badge.update(d.color, statusLabel(d));
    setText(name, d.name || "");
    name.hidden = !d.name;
    setText(ident, [d.device_id, d.remote_addr, platformLabel(d.platform), d.app_version ? "v" + d.app_version : ""].filter(Boolean).join(" · "));
    // "Mati sejak 10.58 · 12 mnt" only while the gate is not simply online.
    setSince(since, d.status === "ONLINE" ? "" : d.status_since, STATUS_TEXT_SINCE[d.status]);
    since.hidden = d.status === "ONLINE" || !d.status_since;
    setAgo(seen, d.last_seen);
    setText(errs1, nf.format(d.errors_1h || 0));
    setText(errs24, nf.format(d.errors_24h || 0));
    setText(outbox, nf.format(d.outbox_pending || 0));
    outboxWrap.hidden = !d.outbox_pending;
    const le = d.last_error;
    last.hidden = !le;
    if (le) {
      lastSev.update(le.severity);
      setText(lastEvent, le.event);
      setText(lastCode, le.error_code);
      setAgo(lastAgo, le.received_at);
    }
    hint.hidden = d.status !== "NEVER";
  };
  return el;
}

function skeletonCards() {
  return h("div", { class: "grid", "aria-hidden": "true" },
    Array.from({ length: 4 }, () => h("div", { class: "card skeleton-card" },
      h("span", { class: "skeleton-text", text: "G00" }),
      h("div", { class: "skeleton-text", text: "STI-G00-POS01 · v0.0.0" }),
      h("div", { class: "skeleton-text", text: "Terakhir terlihat 00 dtk" }))));
}
