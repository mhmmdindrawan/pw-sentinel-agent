// Single-series bar chart: one hue, thin bars anchored to the baseline
// with 4px rounded tops, recessive grid, hover tooltip per bar, and a table
// view for screen readers / exact values. Drawn at the container's real
// pixel width (and redrawn on resize) so text is never stretched.

import { h, svg } from "../core/dom.js";
import { fmtTime, tf } from "../core/format.js";

export function hourlyChart() {
  const wrap = h("div", { class: "chart" });
  const tip = h("div", { class: "tooltip", hidden: true });
  const tbody = h("tbody");
  const table = h("details", { class: "small" }, h("summary", { class: "muted", text: "Lihat sebagai tabel" }),
    h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", { text: "Jam" }), h("th", { class: "num", text: "Error" }))), tbody)));
  const el = h("div", {}, wrap, table);

  let hours = null;
  let key = "";
  let lastW = 0;
  const draw = () => {
    const W = Math.round(wrap.clientWidth);
    if (!W || !hours) return;
    lastW = W;
    wrap.replaceChildren(drawBars(hours, W, tip), tip);
  };
  new ResizeObserver(() => {
    if (Math.round(wrap.clientWidth) !== lastW) draw();
  }).observe(wrap);

  el.update = (data) => {
    const k = JSON.stringify(data);
    if (k === key) return; // unchanged: keep hover state and the open table
    key = k;
    hours = data;
    draw();
    tbody.replaceChildren(...data.map((hr) => h("tr", {}, h("td", { text: fmtTime(hr.hour + ":00:00Z") }), h("td", { class: "num", text: String(hr.count) }))));
  };
  return el;
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
