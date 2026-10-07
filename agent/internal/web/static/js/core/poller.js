// Poller runs a loader on an interval for one view:
// - pauses while the tab is hidden and refreshes as soon as it's visible,
// - backs off after failures (5s, 10s, 30s) without ever clearing the view,
// - reports success/failure to the shared connection state,
// - aborts any in-flight request when stopped (route change).

import { connection } from "./connection.js";
import { AuthError, isAbort } from "./api.js";

const BACKOFF = [5000, 10000, 30000];

export class Poller {
  constructor(load, { interval, onData, onAuthError }) {
    this.load = load;
    this.interval = interval;
    this.onData = onData;
    this.onAuthError = onAuthError;
    this.timer = null;
    this.ctrl = null;
    this.failures = 0;
    this.stopped = false;
    this.onVisibility = () => {
      if (!document.hidden) this.refresh();
    };
  }

  start() {
    document.addEventListener("visibilitychange", this.onVisibility);
    this.refresh();
    return this;
  }

  stop() {
    this.stopped = true;
    clearTimeout(this.timer);
    if (this.ctrl) this.ctrl.abort();
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  async refresh() {
    if (this.stopped) return;
    clearTimeout(this.timer);
    if (this.ctrl) this.ctrl.abort();
    const ctrl = (this.ctrl = new AbortController());
    try {
      const data = await this.load(ctrl.signal);
      if (this.stopped || ctrl.signal.aborted) return;
      this.failures = 0;
      connection.ok(this.interval);
      this.onData(data);
    } catch (err) {
      if (isAbort(err) || this.stopped) return;
      if (err instanceof AuthError) {
        this.stop();
        this.onAuthError(err);
        return;
      }
      this.failures++;
      connection.fail(err);
    }
    this.schedule();
  }

  schedule() {
    if (this.stopped) return;
    const delay = this.failures ? BACKOFF[Math.min(this.failures, BACKOFF.length) - 1] : this.interval;
    // While hidden, wait for visibilitychange instead of polling.
    this.timer = setTimeout(() => {
      if (!document.hidden) this.refresh();
      else this.schedule();
    }, delay);
  }
}
