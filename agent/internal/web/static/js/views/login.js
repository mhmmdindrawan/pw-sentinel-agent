import { h } from "../core/dom.js";
import { api, ApiError, AuthError } from "../core/api.js";
import { typeLoop } from "../core/typing.js";

// mount renders the login form; onSuccess is called once logged in.
export function mount(root, { message, onSuccess }) {
  const msg = h("div", { class: "notice " + (message ? "" : "error-box"), role: "alert", hidden: !message, text: message || "" });
  const user = h("input", { id: "login-user", name: "username", autocomplete: "username", required: true, autocapitalize: "none", spellcheck: "false" });
  const pass = h("input", { id: "login-pass", name: "password", type: "password", autocomplete: "current-password", required: true });
  const submit = h("button", { class: "btn primary", type: "submit", text: "Masuk" });

  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      submit.textContent = "Memeriksa…";
      try {
        const me = await api("/v1/auth/login", { method: "POST", body: { username: user.value.trim(), password: pass.value } });
        onSuccess(me);
      } catch (err) {
        msg.className = "notice error-box";
        // The API layer reports any 401 as AuthError; on this form it means
        // the credentials were wrong.
        msg.textContent = err instanceof AuthError ? "Username atau password salah."
          : err instanceof ApiError && err.status === 429 ? "Terlalu banyak percobaan gagal. Coba lagi dalam 15 menit."
            : "Tidak dapat menghubungi server. Periksa koneksi Tailscale Anda.";
        msg.hidden = false;
        pass.value = "";
        pass.focus();
      } finally {
        submit.disabled = false;
        submit.textContent = "Masuk";
      }
    },
  },
  h("label", { for: "login-user" }, "Username"), user,
  h("label", { for: "login-pass" }, "Password"), pass,
  submit);

  // The version is the running agent's own (set at build time, e.g.
  // deploy/build.sh v1.0.0), read from the public health endpoint so the
  // form never shows a number that differs from what is installed.
  const version = h("span", { class: "login-version", hidden: true });
  const ctrl = new AbortController();
  fetch("/v1/health", { signal: ctrl.signal })
    .then((res) => (res.ok ? res.json() : {}))
    .then((d) => {
      const v = String(d.version || "").replace(/^v(?=\d)/i, "");
      if (v) {
        version.textContent = "Versi " + v;
        version.hidden = false;
      }
    })
    .catch(() => {});

  root.replaceChildren(h("div", { class: "login panel" },
    h("h1", { class: "brand brand-lg" },
      h("span", { class: "brand-name", text: "parkways" }), " ",
      h("span", { class: "brand-sub", text: "Sentinel Gateout Cashless" })),
    h("p", { class: "muted", text: "Masuk untuk memantau status gate." }), msg, form,
    h("footer", { class: "login-foot" },
      version,
      h("span", { text: "© 2026 Parkways Dashboard. All rights reserved." }))));
  const stopTyping = typeLoop(root.querySelector(".brand-sub"));
  user.focus();
  return { destroy() { ctrl.abort(); stopTyping(); } };
}
