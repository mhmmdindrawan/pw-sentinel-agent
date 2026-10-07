"""SDK tests — stdlib unittest only, so they run unmodified on Python 3.6."""

import http.server
import socket
import sys
import threading
import time
import unittest
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from parkways_sentinel import SentinelClient  # noqa: E402


class _Agent(http.server.BaseHTTPRequestHandler):
    status = 201
    received = []

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        type(self).received.append(self.headers.get("Authorization"))
        self.send_response(type(self).status)
        self.end_headers()

    def log_message(self, *args):
        pass


def _start_agent(status):
    handler = type("Agent%d" % status, (_Agent,), {"status": status, "received": []})
    srv = http.server.HTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, handler


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()  # nothing listens here afterwards = "agent down"
    return port


def _drain(client, timeout=10):
    deadline = time.time() + timeout
    while client.pending() and time.time() < deadline:
        time.sleep(0.02)
    client._queue.join()


class OnSendErrorTest(unittest.TestCase):
    def _client(self, url, errors):
        return SentinelClient(device_id="TST-G01", base_url=url, token="t",
                              on_send_error=lambda exc, payload: errors.append((exc, payload)))

    def test_called_when_agent_down(self):
        errors = []
        c = self._client("http://127.0.0.1:%d" % _free_port(), errors)
        c.error("PAYMENT_TIMEOUT")  # must not raise
        _drain(c)
        self.assertEqual(len(errors), 1)
        self.assertEqual(errors[0][1]["event"], "PAYMENT_TIMEOUT")

    def test_called_on_http_error_status(self):
        srv, _ = _start_agent(401)
        self.addCleanup(srv.shutdown)
        errors = []
        c = self._client("http://127.0.0.1:%d" % srv.server_port, errors)
        c.error("PAYMENT_TIMEOUT")
        _drain(c)
        self.assertEqual(len(errors), 1)
        self.assertEqual(getattr(errors[0][0], "code", None), 401)

    def test_not_called_on_success(self):
        srv, handler = _start_agent(201)
        self.addCleanup(srv.shutdown)
        errors = []
        c = self._client("http://127.0.0.1:%d" % srv.server_port, errors)
        c.error("PAYMENT_TIMEOUT")
        _drain(c)
        self.assertEqual(errors, [])
        self.assertEqual(handler.received, ["Bearer t"])

    def test_raising_callback_does_not_kill_worker(self):
        def boom(exc, payload):
            raise RuntimeError("callback bug")
        c = SentinelClient(device_id="TST-G01", base_url="http://127.0.0.1:%d" % _free_port(),
                           on_send_error=boom)
        c.error("A")
        c.error("B")
        _drain(c)
        self.assertTrue(c._thread.is_alive())


if __name__ == "__main__":
    unittest.main()
