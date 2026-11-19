import { h } from "./dom.js";

// typeLoop types el's text letter by letter (each letter fades in), keeps
// the whole line readable for a moment, erases it and starts over — the
// "being typed" look under the brand wordmark. Screen readers get the full
// text once from a visually hidden copy; the animated letters are
// aria-hidden. With prefers-reduced-motion the text stays as it is.
// Returns a stop function.
const TYPE_MS = 90;
const ERASE_MS = 35;
const HOLD_MS = 2800;
const GAP_MS = 500;

export function typeLoop(el) {
  if (!el) return () => {};
  const text = el.textContent.trim();
  if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return () => {};
  const shown = h("span", { class: "typed" });
  const caret = h("span", { class: "caret" });
  // An invisible full copy keeps the box exactly as wide as the whole text
  // in whatever font the PC has, so nothing is ever clipped; the typed
  // letters are laid over it.
  el.replaceChildren(
    h("span", { class: "sr-only", text }),
    h("span", { class: "ghost", "aria-hidden": "true", text }),
    h("span", { class: "typing", "aria-hidden": "true" }, shown, caret));

  let i = 0;
  let erasing = false;
  let timer;
  const next = (ms, busy) => {
    caret.classList.toggle("busy", busy);
    timer = setTimeout(tick, ms);
  };
  function tick() {
    if (!erasing && i < text.length) {
      shown.append(h("span", { class: "ch", text: text[i++] }));
      next(TYPE_MS, true);
    } else if (!erasing) {
      erasing = true;
      next(HOLD_MS, false);
    } else if (i > 0) {
      shown.lastChild.remove();
      i--;
      next(ERASE_MS, true);
    } else {
      erasing = false;
      next(GAP_MS, false);
    }
  }
  next(GAP_MS, false);
  return () => clearTimeout(timer);
}
