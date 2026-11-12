// Relative times ("2 mnt lalu") are rendered as <time data-ago="ISO"> and
// refreshed here, so they stay correct between data refreshes without
// rebuilding any DOM.

import { h, setText } from "./dom.js";
import { fmtAgo, fmtTime, fmtClock, fmtSinceDuration } from "./format.js";

export function agoEl(iso, cls) {
  const el = h("time", { class: cls || null });
  setAgo(el, iso);
  return el;
}

export function setAgo(el, iso) {
  if (el.dataset.ago === (iso || "")) return;
  el.dataset.ago = iso || "";
  if (iso) {
    el.setAttribute("datetime", iso);
    el.title = fmtTime(iso);
  } else {
    el.removeAttribute("datetime");
    el.removeAttribute("title");
  }
  setText(el, fmtAgo(iso));
}

// sinceEl renders "<label> 10.58 · 12 mnt" for a state that began at iso
// and keeps the duration ticking.
export function sinceEl(iso, cls) {
  const el = h("span", { class: cls || null });
  setSince(el, iso, "");
  return el;
}

export function setSince(el, iso, label) {
  const key = (iso || "") + "|" + (label || "");
  if (el.dataset.sinceKey === key) return;
  el.dataset.sinceKey = key;
  el.dataset.since = iso || "";
  el.dataset.sinceLabel = label || "";
  renderSince(el);
}

function renderSince(el) {
  const iso = el.dataset.since;
  if (!iso) { el.replaceChildren(); return; }
  el.replaceChildren(
    (el.dataset.sinceLabel ? el.dataset.sinceLabel + " " : "") + fmtClock(new Date(iso)),
    h("span", { class: "muted", text: " · " + fmtSinceDuration(iso) }));
  el.title = fmtTime(iso);
}

const listeners = new Set();
export function onTick(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

setInterval(() => {
  document.querySelectorAll("time[data-ago]").forEach((el) => setText(el, fmtAgo(el.dataset.ago)));
  document.querySelectorAll("[data-since]").forEach(renderSince);
  listeners.forEach((fn) => fn());
}, 15000);
