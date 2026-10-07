import { h, svg, setText, setClass } from "../core/dom.js";

// Status shapes differ as well as colours (diamond / triangle / circle), so
// the state never depends on colour alone.
const SHAPES = {
  red: () => svg("path", { d: "M6 0.8 L11.2 6 L6 11.2 L0.8 6 Z" }),
  yellow: () => svg("path", { d: "M6 1 L11.4 10.6 H0.6 Z" }),
  green: () => svg("circle", { cx: 6, cy: 6, r: 5 }),
};

export function statusIcon(color) {
  return svg("svg", { class: "ico " + (SHAPES[color] ? color : "green"), viewBox: "0 0 12 12", width: 12, height: 12, "aria-hidden": "true" },
    (SHAPES[color] || SHAPES.green)());
}

// statusBadge returns an element with an update(color, label) method.
export function statusBadge(color, label) {
  const text = h("span");
  const el = h("span", {});
  let current = null;
  el.update = (c, l) => {
    if (c !== current) {
      current = c;
      setClass(el, "status " + c);
      el.replaceChildren(statusIcon(c), text);
    }
    setText(text, l);
  };
  el.update(color, label);
  return el;
}

export function sevBadge(sev) {
  const el = h("span");
  el.update = (s) => {
    setClass(el, "sev " + (s || ""));
    setText(el, s || "—");
  };
  el.update(sev);
  return el;
}
