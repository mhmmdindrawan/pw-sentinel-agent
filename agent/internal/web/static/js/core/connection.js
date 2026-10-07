// Shared connection state behind the banner: when data last arrived, how
// often it should arrive, and whether the last attempt failed.

const listeners = new Set();
const state = { lastOk: null, interval: 15000, error: null };

export const connection = {
  ok(interval) {
    state.lastOk = new Date();
    state.interval = interval;
    state.error = null;
    emit();
  },
  fail(err) {
    state.error = err;
    emit();
  },
  reset() {
    state.lastOk = null;
    state.error = null;
    emit();
  },
  get() {
    return { ...state };
  },
  // isStale: data older than two refresh intervals.
  isStale() {
    return state.lastOk != null && Date.now() - state.lastOk.getTime() > 2 * state.interval;
  },
  subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
};

function emit() {
  listeners.forEach((fn) => fn(connection.get()));
}
