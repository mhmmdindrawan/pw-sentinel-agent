"""SDK tests — stdlib unittest only, so they run unmodified on Python 3.6."""

import http.server
import json
import os
import shutil
import socket
import sqlite3
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from parkways_sentinel import SentinelClient  # noqa: E402


class FakeAgent(object):
    """A local HTTP server that mimics the agent. `mode` can be changed
    while a test runs:

      ok          — accept everything (201 / batch results / heartbeat 200)
      down        — answer 503 to everything (agent trouble)
      nobatch     — like ok, but /v1/events/batch and /v1/heartbeat are 404
      retry       — batch items come back as retryable errors
      status:NNN  — answer NNN to /v1/events and the batch endpoint
    """

    def __init__(self, mode="ok"):
        self.mode = mode
        self.requests = []  # (path, body, auth header)
        self.accepted = []  # events the agent answered 2xx for
        self.lock = threading.Lock()
        agent = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])).decode())
                with agent.lock:
                    agent.requests.append((self.path, body, self.headers.get("Authorization")))
                    mode = agent.mode
                code, resp = agent.answer(mode, self.path, body)
                if code < 300:
                    with agent.lock:
                        if self.path == "/v1/events":
                            agent.accepted.append(body)
                        elif self.path == "/v1/events/batch":
                            agent.accepted.extend(
                                e for e, r in zip(body["events"], resp["results"]) if r["status"] != "error")
                raw = json.dumps(resp).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def log_message(self, *args):
                pass

        self.srv = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        self.url = "http://127.0.0.1:%d" % self.srv.server_port
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    @staticmethod
    def answer(mode, path, body):
        if mode == "down":
            return 503, {"error": "down"}
        if mode.startswith("status:") and path != "/v1/heartbeat":
            return int(mode.split(":")[1]), {"error": "nope"}
        if mode == "nobatch" and path in ("/v1/events/batch", "/v1/heartbeat"):
            return 404, {"error": "not found"}
        if path == "/v1/heartbeat":
            return 200, {"status": "ok"}
        if path == "/v1/events/batch":
            if mode == "retry":
                items = [{"index": i, "status": "error", "error": "disk", "retryable": True} for i in range(len(body["events"]))]
            else:
                items = [{"index": i, "status": "created"} for i in range(len(body["events"]))]
            return 200, {"results": items}
        return 201, {"event_id": body.get("event_id")}

    def events(self):
        """Every event the agent accepted, single or batched."""
        with self.lock:
            return list(self.accepted)

    def paths(self):
        with self.lock:
            return [p for p, _, _ in self.requests]

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()  # nothing listens here afterwards = "agent down"
    return port


def _wait(cond, timeout=5.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if cond():
            return True
        time.sleep(0.02)
    return cond()


class Base(unittest.TestCase):
    def agent(self, mode="ok"):
        a = FakeAgent(mode)
        self.addCleanup(a.close)
        return a

    def client(self, url, **kw):
        kw.setdefault("token", "t")
        kw.setdefault("max_backoff", 0.2)
        kw.setdefault("coalesce_window", 0)
        c = SentinelClient(device_id="TST-G01", base_url=url, **kw)
        self.addCleanup(c.close, 0.5)
        return c


class DeliveryTest(Base):
    def test_delivers_with_token(self):
        a = self.agent()
        c = self.client(a.url)
        c.error("PAYMENT_TIMEOUT", message="x")
        self.assertTrue(c.flush())
        self.assertEqual([e["event"] for e in a.events()], ["PAYMENT_TIMEOUT"])
        self.assertEqual(a.requests[0][2], "Bearer t")

    def test_caller_never_blocks_or_raises_when_agent_down(self):
        c = self.client("http://127.0.0.1:%d" % _free_port())
        t0 = time.time()
        for i in range(1500):  # more than the queue holds
            c.error("PAYMENT_TIMEOUT", message=str(i))
        self.assertLess(time.time() - t0, 1.0)

    def test_backoff_limits_attempts_while_down(self):
        a = self.agent("down")
        c = self.client(a.url, max_backoff=60)
        for i in range(20):
            c.error("E%d" % i)
            time.sleep(0.05)
        # 1s of sending: first try fails, next is 1s later at the earliest.
        self.assertLessEqual(len(a.requests), 2)
        self.assertEqual(c.pending(), 20)

    def test_events_wait_out_an_outage_then_arrive_batched(self):
        a = self.agent("down")
        c = self.client(a.url)
        for i in range(5):
            c.error("E%d" % i)
        _wait(lambda: len(a.requests) >= 1)
        a.mode = "ok"
        self.assertTrue(c.flush())
        names = [e["event"] for e in a.events()]
        for i in range(5):
            self.assertIn("E%d" % i, names)
        self.assertIn("/v1/events/batch", a.paths())

    def test_gap_event_after_reconnect(self):
        a = self.agent("down")
        c = self.client(a.url)
        c.error("PAYMENT_TIMEOUT")
        _wait(lambda: len(a.requests) >= 1)
        time.sleep(0.3)
        a.mode = "ok"
        self.assertTrue(c.flush())
        gaps = [e for e in a.events() if e["event"] == "SENTINEL_GAP"]
        self.assertEqual(len(gaps), 1)
        self.assertEqual(gaps[0]["context"]["dropped"], 0)
        self.assertNotIn("severity", gaps[0])  # nothing lost → registry default (INFO)

    def test_batch_404_falls_back_to_single_posts(self):
        a = self.agent("down")
        c = self.client(a.url)
        for i in range(3):
            c.error("E%d" % i)
        _wait(lambda: len(a.requests) >= 1)
        a.mode = "nobatch"
        self.assertTrue(c.flush())
        self.assertEqual(sorted(e["event"] for e in a.events() if e["event"].startswith("E")), ["E0", "E1", "E2"])
        self.assertFalse(c._batch_supported)

    def test_retryable_batch_errors_are_kept(self):
        a = self.agent("down")
        c = self.client(a.url)
        c.error("A")
        c.error("B")
        _wait(lambda: len(a.requests) >= 1)
        a.mode = "retry"
        _wait(lambda: "/v1/events/batch" in a.paths())
        self.assertEqual(c.pending(), 2)
        a.mode = "ok"
        self.assertTrue(c.flush())


class ErrorReportingTest(Base):
    def test_on_send_error_when_agent_unreachable(self):
        errors = []
        c = self.client("http://127.0.0.1:%d" % _free_port(), on_send_error=lambda e, p: errors.append((e, p)))
        c.error("PAYMENT_TIMEOUT")
        self.assertTrue(_wait(lambda: len(errors) >= 1))
        self.assertEqual(errors[0][1]["event"], "PAYMENT_TIMEOUT")
        self.assertEqual(c.pending(), 1)  # kept for later, not lost

    def test_401_is_reported_and_event_kept(self):
        a = self.agent("status:401")
        errors = []
        c = self.client(a.url, on_send_error=lambda e, p: errors.append(e))
        c.error("PAYMENT_TIMEOUT")
        self.assertTrue(_wait(lambda: len(errors) >= 1))
        self.assertEqual(getattr(errors[0], "code", None), 401)
        self.assertEqual(c.pending(), 1)

    def test_400_is_reported_and_dropped(self):
        a = self.agent("status:400")
        errors = []
        c = self.client(a.url, on_send_error=lambda e, p: errors.append(e))
        c.error("PAYMENT_TIMEOUT")
        self.assertTrue(c.flush())
        self.assertEqual(len(errors), 1)
        self.assertEqual(c.dropped(), 1)

    def test_raising_callback_does_not_kill_worker(self):
        def boom(exc, payload):
            raise RuntimeError("callback bug")

        a = self.agent("down")
        c = self.client(a.url, on_send_error=boom)
        c.error("A")
        _wait(lambda: len(a.requests) >= 1)
        a.mode = "ok"
        self.assertTrue(c.flush())
        self.assertTrue(c._thread.is_alive())


class OutboxTest(Base):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.path = os.path.join(self.dir, "outbox.db")

    def test_survives_client_restart(self):
        down = "http://127.0.0.1:%d" % _free_port()
        c1 = SentinelClient(device_id="TST-G01", base_url=down, outbox_path=self.path, coalesce_window=0)
        for i in range(3):
            c1.error("E%d" % i)
        _wait(lambda: c1._outbox_count == 3)
        c1.close(0.1)

        a = self.agent()
        c2 = self.client(a.url, outbox_path=self.path)
        self.assertTrue(c2.flush())
        self.assertEqual(sorted(e["event"] for e in a.events()), ["E0", "E1", "E2"])

    def test_full_outbox_drops_normal_before_high_priority(self):
        for path in (None, self.path):
            c = self.client("http://127.0.0.1:%d" % _free_port(), outbox_path=path, outbox_max_rows=3)
            c.error("A")
            c.error("PAYMENT_TIMEOUT")  # HIGH in the registry
            c.error("B", severity="HIGH")
            c.error("C")
            c.error("D")
            self.assertTrue(_wait(lambda: c.dropped() == 2))
            if path is None:
                kept = sorted(p["event"] for _, p in c._outbox.peek(10))
            else:  # the client's SQLite connection belongs to its worker thread
                db = sqlite3.connect(path)
                kept = sorted(json.loads(p)["event"] for (p,) in db.execute("SELECT payload FROM outbox"))
                db.close()
            self.assertEqual(kept, ["B", "D", "PAYMENT_TIMEOUT"], "outbox_path=%r" % path)

    def test_unwritable_outbox_falls_back_to_memory(self):
        errors = []
        a = self.agent()
        bad = os.path.join(self.dir, "no", "such", "dir", "outbox.db")
        c = self.client(a.url, outbox_path=bad, on_send_error=lambda e, p: errors.append(e))
        c.error("PAYMENT_TIMEOUT")
        self.assertTrue(c.flush())
        self.assertEqual(len(a.events()), 1)
        self.assertGreaterEqual(len(errors), 1)


class CoalesceTest(Base):
    def test_repeats_become_one_summary(self):
        a = self.agent()
        c = self.client(a.url, coalesce_window=0.5)
        for _ in range(50):
            c.error("READER_DISCONNECTED", device="READER01")
        c.error("PAYMENT_TIMEOUT")  # different problem: not coalesced
        self.assertTrue(_wait(lambda: len(a.events()) >= 3, timeout=3))
        evs = a.events()
        readers = [e for e in evs if e["event"] == "READER_DISCONNECTED"]
        self.assertEqual(len(readers), 2)
        self.assertNotIn("context", readers[0])
        self.assertEqual(readers[1]["context"]["coalesced_count"], 49)
        self.assertEqual(len([e for e in evs if e["event"] == "PAYMENT_TIMEOUT"]), 1)

    def test_single_occurrence_has_no_summary(self):
        a = self.agent()
        c = self.client(a.url, coalesce_window=0.2)
        c.error("READER_DISCONNECTED")
        time.sleep(0.8)
        self.assertEqual(len(a.events()), 1)


class HeartbeatTest(Base):
    def heartbeats(self, a):
        with a.lock:
            return [b for p, b, _ in a.requests if p == "/v1/heartbeat"]

    def test_rate_limited_and_stops_when_app_stops_beating(self):
        a = self.agent()
        c = self.client(a.url, heartbeat_interval=0.3, app_version="3.1.0")
        # The worker checks every 0.5s tick, so over 2s of beating expect a
        # handful of heartbeats — not one per beat() (~200).
        t_end = time.time() + 2.0
        while time.time() < t_end:
            c.beat()
            time.sleep(0.01)
        self.assertTrue(2 <= len(self.heartbeats(a)) <= 7, len(self.heartbeats(a)))
        # Main loop "hangs": the last beat may still earn one final
        # heartbeat, then nothing more however long we wait.
        time.sleep(1.0)
        n = len(self.heartbeats(a))
        time.sleep(1.0)
        self.assertEqual(len(self.heartbeats(a)), n)
        hb = self.heartbeats(a)[0]
        self.assertEqual(hb["app_version"], "3.1.0")
        self.assertEqual(hb["device_id"], "TST-G01")
        self.assertIn("outbox_pending", hb)

    def test_no_beat_no_heartbeat(self):
        a = self.agent()
        self.client(a.url, heartbeat_interval=0.1)
        time.sleep(0.5)
        self.assertEqual(self.heartbeats(a), [])

    def test_old_agent_without_heartbeat_endpoint(self):
        a = self.agent("nobatch")  # 404 on /v1/heartbeat too
        c = self.client(a.url, heartbeat_interval=0.1)
        c.beat()
        _wait(lambda: len(self.heartbeats(a)) == 1)
        c.error("PAYMENT_TIMEOUT")
        self.assertTrue(c.flush(1.0))  # no backoff held the event up
        c.beat()
        time.sleep(0.3)
        self.assertEqual(len(self.heartbeats(a)), 1)  # stopped trying


if __name__ == "__main__":
    unittest.main()
