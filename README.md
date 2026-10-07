# Parkways Sentinel

Error tracking with AI-ready diagnostics for parking gate-out / cashless
payment systems — designed to run **independently of the main payment
server**. Kiosks keep reporting their own errors even if the network, the
central server, or the internet is down.

**Deployment model (MVP):** one central Sentinel Agent runs on a Linux VPS
and serves **every kiosk/gate** — a mix of Lubuntu ARM, Windows, and Linux
x86 machines — over Tailscale. NOC and support use its built-in web
dashboard and get Telegram alerts. Kiosks buffer events locally while the
link is down, so an outage delays reports instead of losing them. Each kiosk authenticates with a per-device
bearer token; the agent resolves location/gate per event from that
authenticated identity, not from its own local config. Full wire protocol:
[`docs/protocol-v1.md`](docs/protocol-v1.md).

What's in this repo:

- **`agent/`** — the Sentinel Agent: one Go binary (no CGO; Linux x86/ARM,
  Windows). It receives events and heartbeats over an authenticated HTTP
  API, normalizes events into standard error codes, stores them in its own
  SQLite database, derives each gate's status (ONLINE/STALE/DOWN, red/
  yellow/green), serves the NOC dashboard, and sends Telegram alerts. No
  dependency on the payment server or its database.
- **`sdk/python/`** — a zero-dependency, Python-3.6-compatible client
  (`parkways_sentinel`) the gate-out application imports. It never blocks
  or crashes the host app; it keeps events in an outbox until the agent
  confirms them, backs off while the agent is unreachable, and coalesces
  repeats of the same problem.
- **`deploy/`** — systemd unit and a multi-target build script.
- **`docs/`** — [wire protocol](docs/protocol-v1.md),
  [VPS deployment runbook](docs/deploy-vps.md),
  [kiosk integration guide](docs/integrasi-kiosk.md),
  [AI-assisted debugging plan](docs/rencana-ai-debugging.md) (not built yet).

Not built yet: incident correlation, AI analyzer and chat, per-site agents
syncing to a central one.

## Error code convention

Every event is stamped with the identity of the gate it happened on and a
stable, human-readable code:

```
<LOCATION>-<GATE>-<CATEGORY>-<NUMBER>
```

Example: `STI-G03-PAY-002` = site STI, gate 03, payment category, error #2
(payment timeout). Location/gate come from the agent's `devices.yaml`,
resolved from the authenticated `device_id` an event carries — the SDK
never has to know or trust its own location label, only a stable
`device_id` and its token (see [`docs/protocol-v1.md`](docs/protocol-v1.md)).

Categories are seeded in [`agent/registry.yaml`](agent/registry.yaml)
(APP, PAY, READER, PRINTER, LPR, CCTV, MQTT, NET, API, DB, DEVICE, SYS).
Any event name your app sends that isn't in the registry yet is still
accepted — the agent allocates the next free number for that category on
first sight and remembers it permanently in its local database, so the
same event always gets the same code, even across restarts.

## Running the agent

Requires Go 1.25+ (toolchain auto-downloads via `go build` if needed).
Production setup on a VPS is in [`docs/deploy-vps.md`](docs/deploy-vps.md).

```bash
cd agent
cp config.example.yaml config.yaml          # set listen_addr, session_secret, public_url
go build -o sentinel-agent ./cmd/sentinel-agent

./sentinel-agent device add -id STI-G03-POS01 -location STI -gate G03 -pos POS01
#   → prints SENTINEL_DEVICE_ID / SENTINEL_TOKEN for that kiosk's config
./sentinel-agent user add -name noc -role viewer   # password on stdin
./sentinel-agent -config config.yaml
```

Then open `http://<listen_addr>/` and log in. Release binaries for every
target: `deploy/build.sh` → `dist/`.

`listen_addr` must be reachable from every kiosk, and nothing else: bind
it to the Tailscale address, never to the public internet (device tokens
are static shared secrets and the dashboard relies on Tailscale for
transport encryption).

Other commands: `device list`, `user list`, `alert test` (sends a test
Telegram message to every configured route), `version`.

### HTTP API

Kiosks (device bearer token, write-only):

- `POST /v1/events` — report one event ([protocol](docs/protocol-v1.md))
- `POST /v1/events/batch` — up to 100 events, per-item results
- `POST /v1/heartbeat` — liveness, app/SDK version, outbox backlog

Dashboard and scripts (login session cookie, or HTTP Basic with a
`users.yaml` user; device tokens are rejected):

- `GET /v1/fleet` — every device with status, colour, last seen, error counts
- `GET /v1/devices/{device_id}` — one device, its 24h summary and hourly counts
- `GET /v1/events` — filter by `device_id`, `location`, `gate`, `error_code`,
  `event`, `severity` (minimum), `since`/`until`; page with `before_id`
- `GET /v1/summary?minutes=30` — counts per error code (`location`, `device_id`)

Public: `GET /v1/health`, and the dashboard page itself (`/`), which holds
no data until you log in.

```bash
curl -u noc "http://100.64.0.1:8787/v1/events?location=STI&severity=HIGH&limit=20"
```

## Using the Python SDK

Full kiosk integration guide (Indonesian): [`docs/integrasi-kiosk.md`](docs/integrasi-kiosk.md).

```python
from parkways_sentinel import SentinelClient, events

monitor = SentinelClient(
    device_id="STI-G03-POS01",
    base_url="http://100.64.0.1:8787",           # the agent's Tailscale address
    token="<SENTINEL_TOKEN from `device add`>",
    app_version="3.9.0",
    outbox_path="/home/kiosk/.bssparking/sentinel_outbox.db",  # survive restarts/outages
)

# in the app's main loop — drives the heartbeat; a hung loop shows as DOWN
monitor.beat()

monitor.error(events.READER_NOT_READY, device="READER01", message="no answer to status poll")

# HTTP failures against the payment server
monitor.api_error(endpoint="/cashless/gateoutcashless", status_code=None, duration_ms=30000)  # timeout

# app-specific events work too; the agent assigns them a stable code
monitor.error("BARRIER_STUCK", category="DEVICE", severity="HIGH",
              message="Boom gate barrier did not respond")
```

`device_id` must be stable across restarts for this physical kiosk — use
the one `sentinel-agent device add` registered. Every call returns
immediately; delivery happens on a background thread and never raises into
the application. Report delivery problems to your own log with
`on_send_error=lambda exc, payload: ...`.

## Development

```bash
# Demo installation with every gate state and 30 days of history
cd agent && go run ./cmd/sentinel-demo -dir /tmp/sentinel-demo -registry registry.yaml
go build -o /tmp/sentinel-agent ./cmd/sentinel-agent
cd /tmp/sentinel-demo && /tmp/sentinel-agent        # http://127.0.0.1:18787, login demo / demo-password-1
go run ./cmd/sentinel-demo -heartbeat http://127.0.0.1:18787   # keeps live demo gates ONLINE

# Tests
cd agent && go test -race ./...
cd sdk/python && python3 -m unittest discover -s tests
cd e2e && npm ci && npx playwright test   # dashboard E2E + axe accessibility (uses system Chrome)
```

The dashboard is plain ES modules in `agent/internal/web/static/js/`
(`core/` fetch, polling, connection state; `ui/` components; `views/`
pages), embedded into the binary — no build step.

## Why decoupled from the main server?

- The payment/transaction database stays a black box to monitoring — no
  customer data, card numbers, balances, or credentials ever pass through
  Sentinel.
- Each gate keeps tracking errors locally even during a full network or
  central-server outage.
- The SDK reports *symbolic events* (`PAYMENT_TIMEOUT`, `READER_DISCONNECTED`,
  HTTP status codes, latency, retry counts) — enough for later aggregation
  and AI root-cause analysis — without ever touching application logs or
  the transaction database directly.
