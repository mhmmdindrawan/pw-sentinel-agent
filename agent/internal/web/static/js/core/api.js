// Thin fetch wrapper that classifies failures, so views can tell "log in
// again" from "server unreachable" from "server said no".

export class AuthError extends Error {}
export class NetworkError extends Error {}
export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

export function isAbort(err) {
  return err && err.name === "AbortError";
}

export async function api(path, { method = "GET", body, signal } = {}) {
  let res;
  try {
    res = await fetch(path, {
      method,
      signal,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch (err) {
    if (isAbort(err)) throw err;
    throw new NetworkError("Server tidak dapat dihubungi");
  }
  if (res.status === 401) throw new AuthError("login required");
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(data.error || "HTTP " + res.status, res.status);
  return data;
}

// latest() wraps an async loader so only the most recent call's result is
// delivered: each call aborts the previous one, and a stale response that
// still arrives is dropped.
export function latest(loader) {
  let ctrl = null;
  let seq = 0;
  const run = async (...args) => {
    if (ctrl) ctrl.abort();
    ctrl = new AbortController();
    const mine = ++seq;
    const result = await loader(ctrl.signal, ...args);
    if (mine !== seq) throw Object.assign(new Error("superseded"), { name: "AbortError" });
    return result;
  };
  run.cancel = () => {
    if (ctrl) ctrl.abort();
    seq++;
  };
  return run;
}
