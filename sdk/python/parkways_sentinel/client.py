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

Kept Python 3.6.9-compatible: no dataclasses, no `X | Y` unions, no walrus
`:=`, no `from __future__ import annotations`, no `asyncio.run` — this SDK
is meant to run unmodified on kiosks stuck on that interpreter.
"""

import json
import queue
import threading
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone

DEFAULT_BASE_URL = "http://127.0.0.1:8787"
DEFAULT_TIMEOUT = 1.0
DEFAULT_QUEUE_SIZE = 1000


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
    ):
        """`device_id` must uniquely and stably identify this kiosk/gate
        machine and must correspond to an entry (and its token) in the
        agent's devices.yaml. `token` is the bearer token for that entry —
        required for the agent to accept events in practice, but kept
        optional here so local/dev usage against an unauthenticated test
        agent doesn't break; the production agent 401s any request without
        a valid token."""
        self._device_id = device_id
        self._url = base_url.rstrip("/") + "/v1/events"
        self._token = token
        self._app_version = app_version
        self._timeout = timeout
        self._on_send_error = on_send_error
        self._queue = queue.Queue(maxsize=queue_size)
        self._thread = threading.Thread(
            target=self._worker, name="parkways-sentinel-sender", daemon=True
        )
        self._thread.start()

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
            "occurred_at": datetime.now(timezone.utc).isoformat(),
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

    def pending(self):
        """Number of events queued but not yet sent (or dropped)."""
        return self._queue.qsize()

    def _enqueue(self, payload):
        try:
            self._queue.put_nowait(payload)
        except queue.Full:
            pass  # never block the caller; drop the event instead

    def _worker(self):
        while True:
            payload = self._queue.get()
            try:
                self._send(payload)
            except Exception as exc:  # noqa: BLE001 - must never propagate
                if self._on_send_error is not None:
                    try:
                        self._on_send_error(exc, payload)
                    except Exception:
                        pass
            finally:
                self._queue.task_done()

    def _send(self, payload):
        data = json.dumps(payload).encode("utf-8")
        headers = {"Content-Type": "application/json"}
        if self._token:
            headers["Authorization"] = "Bearer " + self._token
        req = urllib.request.Request(self._url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self._timeout):
                pass
        except (urllib.error.URLError, TimeoutError, OSError):
            pass  # agent unreachable/down — swallow, never impact the host app
