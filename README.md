# Parkways Sentinel

Error tracking with AI-ready diagnostics for parking gate-out / cashless
payment systems — designed to run **independently of the main payment
server**. Kiosks keep reporting their own errors even if the network, the
central server, or the internet is down.

**Deployment model:** one Sentinel Agent instance runs on a PC on the same
LAN as the kiosks (or reachable remotely over a VPN like Tailscale) and
serves **many kiosks/gates at once** — a mix of Lubuntu ARM, Windows, and
Linux x86 machines is expected. Each kiosk authenticates with a per-device
bearer token; the agent resolves location/gate per event from that
authenticated identity, not from its own local config. Full wire protocol:
[`docs/protocol-v1.md`](docs/protocol-v1.md).

This repo currently contains the first slice of the system:

- **`agent/`** — the Sentinel Agent: a small Go service. It receives events
  over an authenticated HTTP API, resolves each event's kiosk identity,
  normalizes it into a standard error code, and stores it in a local
  SQLite database. No dependency on the payment server or its database.
- **`sdk/python/`** — a zero-dependency, Python-3.6.9-compatible client
  (`parkways_sentinel`) that a gate-out application imports to report
  events to the agent, fire-and-forget, without ever blocking or crashing
  the host app.

Not built yet (future components, out of scope for this slice):
heartbeat/device inventory, aggregator/incident correlation, AI analyzer,
dashboard, central/hybrid sync across locations.

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

```bash
cd agent
cp config.example.yaml config.yaml
cp devices.example.yaml devices.yaml
# edit devices.yaml: add one entry per kiosk (device_id, location, gate, pos, token)
go build -o sentinel-agent ./cmd/sentinel-agent
./sentinel-agent -config config.yaml
```

`listen_addr` defaults to `0.0.0.0:8787` — it must be reachable from every
kiosk this agent monitors. Firewall it to the LAN/VPN segment the kiosks
live on; do not expose it to the public internet (Tahap 1's bearer tokens
are static, non-rotating shared secrets, not short-lived credentials).

### HTTP API

- `POST /v1/events` — report an event, requires `Authorization: Bearer <token>` (see [`docs/protocol-v1.md`](docs/protocol-v1.md))
- `GET /v1/events?limit=100` — most recent stored events
- `GET /v1/summary?minutes=30` — counts per error code in the time window
- `GET /v1/health` — liveness + configured device count

Example:

```bash
curl -X POST http://127.0.0.1:8787/v1/events \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer REPLACE_ME_WITH_A_LONG_RANDOM_SECRET" \
  -d '{
        "device_id": "KIOSK-STI-G03",
        "event_id": "b6d9e2b0-1c3a-4e9a-9b2e-2a6f6a1e0c11",
        "occurred_at": "2026-09-26T19:40:21+08:00",
        "event": "PAYMENT_TIMEOUT",
        "message": "Payment API timeout",
        "device": "PAYMENT",
        "transaction_ref": "8629730810",
        "retry_count": 3,
        "duration_ms": 3200,
        "app_version": "3.9.0"
      }'
```

→ stored as `STI-G03-PAY-002` — location/gate come from `devices.yaml` via
the authenticated `device_id`, not the request body's location/gate (there
is none) or a single agent-wide config.

## Using the Python SDK

```python
from parkways_sentinel import SentinelClient, events

monitor = SentinelClient(
    device_id="KIOSK-STI-G03",
    base_url="http://192.168.1.50:8787",  # the LAN address of the Sentinel Agent PC
    token="REPLACE_ME_WITH_A_LONG_RANDOM_SECRET",
    app_version="3.9.0",
)

# generic event
monitor.error(
    events.PAYMENT_TIMEOUT,
    message="Payment API timeout",
    device="PAYMENT",
    transaction_ref="8629730810",
    retry_count=3,
    duration_ms=3200,
)

# convenience wrapper for HTTP/API failures
monitor.api_error(
    endpoint="/cashless/gateoutcashless",
    status_code=500,
    duration_ms=2840,
)

# any app-specific event works too, even if not in registry.yaml yet
monitor.error("BARRIER_STUCK", category="DEVICE", severity="HIGH",
               message="Boom gate barrier did not respond")
```

`device_id` must be a value that's stable across restarts for this
physical kiosk (an MQTT device serial, `/etc/machine-id` on Linux, a
registry `MachineGuid` on Windows, etc.) — obtaining it is the host
application's responsibility, not the SDK's, since it's platform-specific.
Every `event_id`/`occurred_at` needed for idempotency and dual-timestamping
is generated by the SDK automatically.

Every call is enqueued and sent from a background thread. If the agent is
down or unreachable, the SDK silently drops the event and returns
immediately — it will never raise an exception into, or slow down, your
gate-out application.

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
