"""Client for reporting events to a Parkways Sentinel Agent.

Design goal: this client must NEVER slow down or crash the host gate-out
application, even if the agent process is down, unreachable, or slow.
Every call enqueues a payload and returns immediately; a background thread
does the actual HTTP send and swallows any failure.

One agent instance can serve many kiosks/gates (see docs/protocol-v1.md in
the parkways-sentinel-agent repo), so every event must carry a `device_id`
that uniquely and stably identifies this physical machine, and must
authenticate with the bearer `token` assigned to that device_id in the
agent's devices.yaml. Obtaining a stable device_id is platform/app-specific
(e.g. an MQTT device serial, /etc/machine-id on Linux, MachineGuid on
Windows) and is deliberately the calling application's responsibility, not
this SDK's — pass whatever your app already uses to identify itself.

Delivery, since the agent now sits across a WAN link (Tailscale to a VPS):

- Events pass through an outbox (in memory, or a SQLite file when
  `outbox_path` is set) and leave it only once the agent acknowledged them,
  so an outage delays events instead of losing them. Retries are safe:
  every event carries an `event_id` the agent deduplicates on.
- After a failed request the worker backs off (1 s, 2 s, 4 s … up to
  `max_backoff`) instead of trying again on every event.
- A backlog is drained through POST /v1/events/batch.
- The same problem repeating within `coalesce_window` seconds is sent once
  right away, then once more as a summary with a count, rather than once
  per occurrence (e.g. a polling loop reporting a dead reader every cycle).
- When the link comes back after an outage, a SENTINEL_GAP event reports
  how long it was down and whether anything was lost.
- `beat()`, called from the app's own main loop, sends a heartbeat at most
  once per `heartbeat_interval`; if the main loop hangs, heartbeats stop
  and the dashboard shows the gate going STALE/DOWN.

Kept Python 3.6.9-compatible: no dataclasses, no `X | Y` unions, no walrus
`:=`, no `from __future__ import annotations`, no `asyncio.run` — this SDK
is meant to run unmodified on kiosks stuck on that interpreter.
"""

import json
import queue
import threading
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone

from .events import HIGH_PRIORITY, SENTINEL_GAP
from .outbox import MemoryOutbox, SqliteOutbox

SDK_VERSION = "0.2.0"

DEFAULT_BASE_URL = "http://127.0.0.1:8787"
DEFAULT_TIMEOUT = 2.0
DEFAULT_QUEUE_SIZE = 1000

# Worker pacing: how often it wakes when idle, and the minimum gap between
# two outbox writes (each write is one SQLite transaction).
_TICK = 0.5
_COMMIT_INTERVAL = 1.0
# Requests per worker iteration before it goes back to collecting events.
_MAX_REQUESTS_PER_ROUND = 10
# The agent accepts at most this many events per batch request.
_MAX_BATCH = 100
# How often old events are purged from the outbox.
_EXPIRE_INTERVAL = 60.0


class _PermanentError(Exception):
    """The agent rejected the payload itself (4xx other than 401/404);
    sending it again can't succeed, so it is dropped."""


def _now_iso():
    return datetime.now(timezone.utc).isoformat()


class SentinelClient:
    def __init__(
        self,
        device_id,
        base_url=DEFAULT_BASE_URL,
        token=None,
        app_version=None,
        timeout=DEFAULT_TIMEOUT,
        queue_size=DEFAULT_QUEUE_SIZE,
        on_send_error=None,
        outbox_path=None,
        outbox_max_rows=10000,
        outbox_max_age_days=7,
        coalesce_window=60.0,
        heartbeat_interval=60.0,
        max_backoff=60.0,
        batch_size=50,
    ):
        """`device_id` must uniquely and stably identify this kiosk/gate
        machine and must correspond to an entry (and its token) in the
        agent's devices.yaml. `token` is the bearer token for that entry —
        required for the agent to accept events in practice, but kept
        optional here so local/dev usage against an unauthenticated test
        agent doesn't break; the production agent 401s any request without
        a valid token.

        `on_send_error(exc, payload)` is called from the worker thread after
        a failed request (payload is the first event involved, or None for a
        heartbeat/outbox failure). Exceptions it raises are ignored.

        `outbox_path`: a SQLite file to keep undelivered events across app
        restarts. None keeps them in memory only. If the file can't be
        opened or written, the client falls back to memory and reports the
        error through on_send_error."""
        self._device_id = device_id
        base = base_url.rstrip("/")
        self._url_event = base + "/v1/events"
        self._url_batch = base + "/v1/events/batch"
        self._url_heartbeat = base + "/v1/heartbeat"
        self._token = token
        self._app_version = app_version
        self._timeout = timeout
        self._on_send_error = on_send_error
        self._queue = queue.Queue(maxsize=queue_size)

        self._outbox_path = outbox_path
        self._outbox_max_rows = outbox_max_rows
        self._outbox_max_age = outbox_max_age_days * 86400.0
        self._outbox = None  # opened by the worker thread (SQLite objects are thread-bound)
        self._outbox_count = 0
        self._batch_size = max(1, min(batch_size, _MAX_BATCH))
        self._batch_supported = True
        self._last_expire = 0.0

        self._max_backoff = max_backoff
        self._failures = 0
        self._next_attempt = 0.0  # monotonic
        self._offline_since = None  # wall clock of the first failure in a row
        self._dropped = 0  # events discarded since start (queue/outbox full, expired, rejected)
        self._dropped_at_offline = 0

        self._coalesce_window = coalesce_window
        self._coalesce_lock = threading.Lock()
        self._windows = {}  # key -> {"start", "count", "first", "last", "template"}

        self._heartbeat_interval = heartbeat_interval
        self._heartbeat_supported = True
        self._started = time.monotonic()
        self._last_beat = None  # monotonic time of the app's last beat()
        self._last_heartbeat = None  # monotonic time we last sent one

        self._stop = threading.Event()
        self._ready = threading.Event()  # set once the worker opened the outbox
        self._thread = threading.Thread(
            target=self._worker, name="parkways-sentinel-sender", daemon=True
        )
        self._thread.start()

    # ------------------------------------------------------------------
    # Public API — safe to call from any thread, never blocks, never raises.

    def error(
        self,
        event,
        message=None,
        category=None,
        severity=None,
        device=None,
        transaction_ref=None,
        retry_count=None,
        endpoint=None,
        http_status=None,
        duration_ms=None,
        app_version=None,
        **context,
    ):
        """Report a single event. Extra keyword arguments are sent as
        free-form context (e.g. reader_id="READER01", latency_ms=2840)."""
        payload = {
            "event": event,
            "device_id": self._device_id,
            "event_id": str(uuid.uuid4()),
            "occurred_at": _now_iso(),
        }
        if message is not None:
            payload["message"] = message
        if category is not None:
            payload["category"] = category
        if severity is not None:
            payload["severity"] = severity
        if device is not None:
            payload["device"] = device
        if transaction_ref is not None:
            payload["transaction_ref"] = transaction_ref
        if retry_count is not None:
            payload["retry_count"] = retry_count
        if endpoint is not None:
            payload["endpoint"] = endpoint
        if http_status is not None:
            payload["http_status"] = http_status
        if duration_ms is not None:
            payload["duration_ms"] = duration_ms

        version = app_version or self._app_version
        if version:
            payload["app_version"] = version
        if context:
            payload["context"] = context

        if self._coalesce(payload):
            self._enqueue(payload)

    def api_error(self, endpoint, status_code=None, duration_ms=None, event=None, message=None, **context):
        """Convenience wrapper for HTTP call failures against the payment
        server. Picks a sensible event name from the status code if one
        isn't given explicitly."""
        if event is None:
            if status_code is None:
                event = "HTTP_TIMEOUT"
            elif status_code >= 500:
                event = "HTTP_SERVER_ERROR"
            else:
                event = "HTTP_CLIENT_ERROR"

        self.error(
            event=event,
            category="API",
            endpoint=endpoint,
            http_status=status_code,
            duration_ms=duration_ms,
            message=message,
            **context,
        )

    def beat(self):
        """Call this from the application's main loop (every cycle is
        fine). A heartbeat goes out at most once per heartbeat_interval, and
        only while beats keep coming — so a hung main loop shows up on the
        dashboard."""
        self._last_beat = time.monotonic()

    def pending(self):
        """Number of events not yet acknowledged by the agent."""
        return self._queue.qsize() + self._outbox_count

    def dropped(self):
        """Number of events discarded since this client started."""
        return self._dropped

    def flush(self, timeout=5.0):
        """Wait up to `timeout` seconds for everything queued so far to be
        delivered. Returns True if nothing is pending. For shutdown paths
        and tests; never needed in normal operation."""
        deadline = time.monotonic() + timeout
        # Until the worker has opened the outbox, events persisted by an
        # earlier run aren't counted in pending() yet.
        self._ready.wait(timeout)
        while time.monotonic() < deadline:
            if self.pending() == 0:
                return True
            time.sleep(0.02)
        return self.pending() == 0

    def close(self, timeout=2.0):
        """Stop the worker after a last delivery attempt. Undelivered events
        stay in the SQLite outbox (if any) for the next start."""
        self.flush(timeout)
        self._stop.set()
        self._thread.join(timeout)

    # ------------------------------------------------------------------
    # Caller-side helpers (run on the application's thread; no I/O).

    def _enqueue(self, payload):
        try:
            self._queue.put_nowait(payload)
        except queue.Full:
            self._dropped += 1  # never block the caller; drop the event instead

    def _coalesce(self, payload):
        """Return True if this event should be sent now. Repeats of the same
        problem within the window are only counted; the worker sends one
        summary for them when the window closes."""
        if self._coalesce_window <= 0:
            return True
        key = (
            payload["event"],
            payload.get("device"),
            payload.get("category"),
            payload.get("endpoint"),
            payload.get("http_status"),
        )
        now = time.monotonic()
        with self._coalesce_lock:
            w = self._windows.get(key)
            if w is None or now - w["start"] >= self._coalesce_window:
                summary = self._close_window(w) if w is not None else None
                self._windows[key] = {"start": now, "count": 0, "first": None, "last": None, "template": payload}
            else:
                w["count"] += 1
                w["last"] = payload["occurred_at"]
                if w["first"] is None:
                    w["first"] = payload["occurred_at"]
                return False
        if summary is not None:
            self._enqueue(summary)
        return True

    def _close_window(self, w):
        """Build the summary event for a closed window, or None if nothing
        repeated. Caller holds _coalesce_lock."""
        if w["count"] == 0:
            return None
        s = dict(w["template"])
        s["event_id"] = str(uuid.uuid4())
        s["occurred_at"] = w["last"]
        s["message"] = "repeated %d more time(s) after the first report" % w["count"]
        ctx = dict(s.get("context") or {})
        ctx.update({"coalesced_count": w["count"], "first_seen": w["first"], "last_seen": w["last"]})
        s["context"] = ctx
        return s

    def _flush_windows(self, now, force=False):
        summaries = []
        with self._coalesce_lock:
            for key in list(self._windows):
                w = self._windows[key]
                if force or now - w["start"] >= self._coalesce_window:
                    s = self._close_window(w)
                    if s is not None:
                        summaries.append(s)
                    del self._windows[key]
        return summaries

    # ------------------------------------------------------------------
    # Worker thread.

    def _worker(self):
        self._outbox = self._open_outbox()
        self._ready.set()
        last_commit = 0.0
        while not self._stop.is_set():
            try:
                incoming = self._collect(last_commit)
                now = time.monotonic()
                incoming.extend(self._flush_windows(now))
                if incoming:
                    self._store(incoming)
                    last_commit = time.monotonic()
                self._expire()
                if time.monotonic() >= self._next_attempt:
                    self._deliver()
                if time.monotonic() >= self._next_attempt:
                    self._maybe_heartbeat()
            except Exception as exc:  # noqa: BLE001 - the worker must never die
                self._report(exc, None)
                self._stop.wait(_TICK)
        # Shutting down: persist anything still queued or coalesced.
        try:
            leftover = self._drain_queue()
            leftover.extend(self._flush_windows(time.monotonic(), force=True))
            self._store(leftover)
            self._outbox.close()
        except Exception:
            pass

    def _open_outbox(self):
        if self._outbox_path:
            try:
                ob = SqliteOutbox(self._outbox_path, self._outbox_max_rows)
                self._outbox_count = ob.count()
                return ob
            except Exception as exc:
                self._report(exc, None)
        return MemoryOutbox(self._outbox_max_rows)

    def _collect(self, last_commit):
        """Wait for events, then gather everything queued — but write at
        most once per _COMMIT_INTERVAL so a burst becomes one transaction."""
        try:
            first = self._queue.get(timeout=_TICK)
        except queue.Empty:
            return []
        wait = _COMMIT_INTERVAL - (time.monotonic() - last_commit)
        if wait > 0 and self._outbox_path:
            self._stop.wait(wait)
        items = [first]
        items.extend(self._drain_queue())
        return items

    def _drain_queue(self):
        items = []
        while True:
            try:
                items.append(self._queue.get_nowait())
            except queue.Empty:
                return items

    def _store(self, payloads):
        if not payloads:
            return
        rows = [(p, 1 if self._is_high_priority(p) else 0) for p in payloads]
        try:
            self._dropped += self._outbox.add_many(rows, time.time())
        except Exception as exc:
            # Disk full / file broken: keep going in memory. Rows already on
            # disk stay there for the next start.
            self._report(exc, None)
            self._outbox = MemoryOutbox(self._outbox_max_rows)
            self._dropped += self._outbox.add_many(rows, time.time())
        self._outbox_count = self._outbox.count()

    @staticmethod
    def _is_high_priority(payload):
        sev = (payload.get("severity") or "").upper()
        return payload.get("event") in HIGH_PRIORITY or sev in ("HIGH", "CRITICAL")

    def _expire(self):
        now = time.monotonic()
        if now - self._last_expire < _EXPIRE_INTERVAL:
            return
        self._last_expire = now
        n = self._outbox.expire(time.time() - self._outbox_max_age)
        if n:
            self._dropped += n
            self._outbox_count = self._outbox.count()

    def _deliver(self):
        for _ in range(_MAX_REQUESTS_PER_ROUND):
            n = self._batch_size if self._batch_supported else 1
            rows = self._outbox.peek(n)
            if not rows:
                return
            try:
                if len(rows) > 1:
                    done, retry = self._send_batch(rows)
                else:
                    done, retry = self._send_one(rows[0])
            except Exception as exc:
                self._on_failure(exc, rows[0][1])
                return
            self._outbox.delete(done)
            self._outbox_count = self._outbox.count()
            if retry:
                # The agent stored some but failed on others (e.g. its disk);
                # keep those and back off before trying again.
                self._on_failure(RuntimeError("agent asked to retry %d event(s)" % len(retry)), rows[0][1])
                return
            self._on_success()
            if self._stop.is_set():
                return

    def _send_one(self, row):
        row_id, payload = row
        try:
            self._post(self._url_event, payload)
        except _PermanentError as exc:
            self._dropped += 1
            self._report(exc, payload)
        return [row_id], []

    def _send_batch(self, rows):
        try:
            resp = self._post(self._url_batch, {"events": [p for _, p in rows]})
        except _PermanentError:
            # The agent refused the batch as a whole (e.g. a stricter size
            # limit): fall back to one by one, which reports per event.
            self._batch_supported = False
            return [], []
        except urllib.error.HTTPError as exc:
            if exc.code == 404:
                # Agent predates the batch endpoint: fall back to one by one.
                self._batch_supported = False
                return [], []
            raise
        done, retry = [], []
        results = resp.get("results") if isinstance(resp, dict) else None
        if not isinstance(results, list) or len(results) != len(rows):
            raise RuntimeError("malformed batch response")
        for (row_id, payload), res in zip(rows, results):
            if res.get("status") == "error":
                if res.get("retryable"):
                    retry.append(row_id)
                    continue
                self._dropped += 1
                self._report(_PermanentError(res.get("error")), payload)
            done.append(row_id)
        return done, retry

    def _maybe_heartbeat(self):
        if self._last_beat is None or self._heartbeat_interval <= 0 or not self._heartbeat_supported:
            return
        now = time.monotonic()
        if self._last_heartbeat is not None:
            if self._last_beat <= self._last_heartbeat:
                return  # no beat since the last heartbeat: the app may be hung
            if now - self._last_heartbeat < self._heartbeat_interval:
                return
        body = {
            "device_id": self._device_id,
            "sdk_version": SDK_VERSION,
            "uptime_s": int(now - self._started),
            "outbox_pending": self.pending(),
            "outbox_dropped": self._dropped,
            "sent_at": _now_iso(),
        }
        if self._app_version:
            body["app_version"] = self._app_version
        try:
            self._post(self._url_heartbeat, body)
        except urllib.error.HTTPError as exc:
            if exc.code == 404:
                # Agent predates heartbeats. Not an outage: don't back off
                # (that would hold up events too), just stop sending them.
                self._heartbeat_supported = False
                return
            self._on_failure(exc, None)
            return
        except Exception as exc:
            self._on_failure(exc, None)
            return
        self._last_heartbeat = now
        self._on_success()

    def _post(self, url, body):
        data = json.dumps(body).encode("utf-8")
        headers = {"Content-Type": "application/json"}
        if self._token:
            headers["Authorization"] = "Bearer " + self._token
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            # 401: token not (yet) on the agent; 404: endpoint missing;
            # 5xx: agent trouble — all worth retrying later. Any other 4xx
            # means this payload itself is unacceptable.
            if 400 <= exc.code < 500 and exc.code not in (401, 404, 408, 429):
                raise _PermanentError("HTTP %d from agent" % exc.code)
            raise
        try:
            return json.loads(raw.decode("utf-8")) if raw else None
        except ValueError:
            return None

    def _on_failure(self, exc, payload):
        self._failures += 1
        delay = min(self._max_backoff, 2.0 ** (self._failures - 1))
        self._next_attempt = time.monotonic() + delay
        if self._offline_since is None:
            self._offline_since = time.time()
            self._dropped_at_offline = self._dropped
        self._report(exc, payload)

    def _on_success(self):
        self._failures = 0
        self._next_attempt = 0.0
        if self._offline_since is None:
            return
        offline_s = int(time.time() - self._offline_since)
        lost = self._dropped - self._dropped_at_offline
        self._offline_since = None
        gap = {
            "event": SENTINEL_GAP,
            "device_id": self._device_id,
            "event_id": str(uuid.uuid4()),
            "occurred_at": _now_iso(),
            "message": "agent unreachable for %ds; %d event(s) waiting, %d lost" % (offline_s, self.pending(), lost),
            "context": {"offline_seconds": offline_s, "pending_at_reconnect": self.pending(), "dropped": lost},
        }
        if lost:
            gap["severity"] = "MEDIUM"
        if self._app_version:
            gap["app_version"] = self._app_version
        self._store([gap])

    def _report(self, exc, payload):
        if self._on_send_error is None:
            return
        try:
            self._on_send_error(exc, payload)
        except Exception:
            pass
