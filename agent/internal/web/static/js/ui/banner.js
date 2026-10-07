// Connection banner under the top bar: appears when a refresh fails or the
// data on screen is older than two refresh intervals, and goes away by
// itself when data flows again. It never hides the data underneath.

import { h, setText } from "../core/dom.js";
import { connection } from "../core/connection.js";
import { fmtAgo, fmtClock } from "../core/format.js";
import { onTick } from "../core/ticker.js";

export function mountBanner(el) {
  const text = h("span");
  const retry = h("button", { type: "button", class: "link", text: "Coba sekarang" });
  el.replaceChildren(text, retry);

  let onRetry = null;
  retry.addEventListener("click", () => onRetry && onRetry());

  const render = () => {
    const st = connection.get();
    const stale = connection.isStale();
    if (!st.error && !stale) {
      el.hidden = true;
      return;
    }
    const since = st.lastOk ? ` · data terakhir ${fmtClock(st.lastOk)} (${fmtAgo(st.lastOk.toISOString())})` : "";
    const what = st.error ? "Koneksi ke server Sentinel terputus" : "Data belum diperbarui";
    setText(text, `${what}${since} · mencoba lagi otomatis`);
    el.hidden = false;
  };
  connection.subscribe(render);
  onTick(render);
  render();
  return { setRetry: (fn) => (onRetry = fn) };
}
