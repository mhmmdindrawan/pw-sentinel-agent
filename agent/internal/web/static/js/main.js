// Parkways Sentinel dashboard — entry point and hash router.
//
// Each view exports mount(root, params, ctx) → { destroy, poller? }. The
// router destroys the previous view (stopping its polling and in-flight
// requests) before mounting the next one.

import { h } from "./core/dom.js";
import { api, AuthError } from "./core/api.js";
import { connection } from "./core/connection.js";
import { mountBanner } from "./ui/banner.js";
import * as login from "./views/login.js";
import * as fleet from "./views/fleet.js";
import * as device from "./views/device.js";
import * as events from "./views/events.js";

const main = document.getElementById("app");
const topbar = document.getElementById("topbar");
const whoName = document.getElementById("who-name");
const banner = mountBanner(document.getElementById("conn"));

const ROUTES = [
  { re: /^\/device\/(.+)$/, view: device, nav: "fleet", title: "Detail gate", params: (m) => ({ id: decodeURIComponent(m[1]) }) },
  { re: /^\/events$/, view: events, nav: "events", title: "Event" },
  { re: /^\/?$/, view: fleet, nav: "fleet", title: "Ringkasan" },
];

let me = null;
let current = null;
let firstRoute = true;

function parseHash() {
  const hash = location.hash.slice(1) || "/";
  const [path, query] = hash.split("?");
  return { path, query: Object.fromEntries(new URLSearchParams(query || "")) };
}

function unmount() {
  if (current) current.destroy();
  current = null;
}

function showLogin(message) {
  unmount();
  me = null;
  connection.reset();
  topbar.hidden = true;
  document.title = "Masuk — Parkways Sentinel";
  current = login.mount(main, {
    message,
    onSuccess: (user) => {
      me = user;
      // The login form (and its focused button) is gone: put focus on the
      // page heading so Tab continues from the top of the new content.
      firstRoute = false;
      route();
    },
  });
}

function onAuthError() {
  showLogin("Sesi Anda berakhir. Silakan masuk lagi — Anda akan kembali ke halaman ini.");
}

function showUnreachable() {
  unmount();
  topbar.hidden = true;
  main.replaceChildren(h("div", { class: "login panel" },
    h("h1", { text: "Server tidak dapat dihubungi" }),
    h("p", { class: "muted", text: "Dashboard tidak bisa menjangkau server Sentinel. Pastikan Tailscale aktif di perangkat ini." }),
    h("button", { type: "button", class: "btn primary", text: "Coba lagi", onclick: route })));
}

async function route() {
  unmount();
  if (!me) {
    try {
      me = await api("/v1/auth/me");
    } catch (err) {
      if (err instanceof AuthError) return showLogin();
      return showUnreachable();
    }
  }
  topbar.hidden = false;
  whoName.textContent = me.username;

  const { path, query } = parseHash();
  let r = ROUTES[ROUTES.length - 1];
  let m = null;
  for (const cand of ROUTES) {
    m = path.match(cand.re);
    if (m) { r = cand; break; }
  }
  document.querySelectorAll("[data-nav]").forEach((a) => {
    const active = a.dataset.nav === r.nav;
    a.classList.toggle("active", active);
    if (active) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  });
  document.title = r.title + " — Parkways Sentinel";
  const params = Object.assign({}, query, r.params && m ? r.params(m) : {});
  current = r.view.mount(main, params, { onAuthError });
  banner.setRetry(() => current && current.poller && current.poller.refresh());

  window.scrollTo(0, 0);
  // Move focus to the new page's heading so keyboard and screen-reader
  // users land on the new content — but not on first load, where the
  // browser's own focus handling is expected.
  const heading = main.querySelector("h1");
  if (heading && !firstRoute) heading.focus({ preventScroll: true });
  firstRoute = false;
}

document.getElementById("logout").addEventListener("click", async () => {
  try { await api("/v1/auth/logout", { method: "POST" }); } catch (_) { /* logging out locally regardless */ }
  showLogin("Anda sudah keluar.");
});

document.getElementById("skip").addEventListener("click", (e) => {
  e.preventDefault(); // a real #anchor would trigger the hash router
  const heading = main.querySelector("h1");
  if (heading) heading.focus();
});

// The connection banner sticks right under the top bar, whose height
// changes when it wraps on phones.
new ResizeObserver(() => {
  document.documentElement.style.setProperty("--topbar-h", topbar.offsetHeight + "px");
}).observe(topbar);

window.addEventListener("hashchange", route);
route();
