// Relative times ("2 mnt lalu") are rendered as <time data-ago="ISO"> and
// refreshed here, so they stay correct between data refreshes without
// rebuilding any DOM.

import { h, setText } from "./dom.js";
import { fmtAgo, fmtTime } from "./format.js";

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

const listeners = new Set();
export function onTick(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

setInterval(() => {
  document.querySelectorAll("time[data-ago]").forEach((el) => setText(el, fmtAgo(el.dataset.ago)));
  listeners.forEach((fn) => fn());
}, 15000);
