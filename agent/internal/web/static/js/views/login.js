import { h } from "../core/dom.js";
import { api, ApiError, AuthError } from "../core/api.js";

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

  root.replaceChildren(h("div", { class: "login panel" },
    h("h1", { text: "Parkways Sentinel" }),
    h("p", { class: "muted", text: "Masuk untuk memantau status gate." }), msg, form));
  user.focus();
  return { destroy() {} };
}
