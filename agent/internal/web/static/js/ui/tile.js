import { h, setText } from "../core/dom.js";

// tile is a stat tile whose value/sub text can be updated in place. Before
// the first update it shows a skeleton.
export function tile(label, { icon, sub } = {}) {
  const value = h("div", { class: "value skeleton-text", text: "00" });
  const subEl = h("div", { class: "sub", text: sub || "" });
  const el = h("div", { class: "tile" }, h("div", { class: "label" }, icon, label), value, subEl);
  el.update = (v, s) => {
    value.classList.remove("skeleton-text");
    setText(value, v);
    if (s !== undefined) setText(subEl, s);
  };
  return el;
}
