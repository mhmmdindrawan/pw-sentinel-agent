# Parkways Sentinel — Event Protocol v1

This document specifies the wire format for `POST /v1/events` between any
SDK/client and a Sentinel Agent. It is the contract both sides must honor —
implementations should read this before touching code that builds or parses
event payloads.

## Deployment model this protocol assumes

One agent instance now serves **multiple kiosks/gates** (Lubuntu ARM,
Windows, and Linux x86 machines) over the LAN or a VPN like Tailscale — it
is no longer one agent per gate. Because of this, an event no longer
carries an implicit location/gate from "whichever agent process received
it"; the agent resolves location/gate **per event**, from an authenticated
`device_id`.

Since 0.5 the intended layout is **one agent per site**: it runs on the
location's local server (PCC with 2 gates, PJT with 4, …) and only tracks
the gates of that site. `site.code` in config.yaml is the location every
enrolled kiosk is attributed to; the dashboard header shows
`CODE · name` and the agent's Tailscale IP so support knows where they are
looking. A central agent on a VPS still works (static `devices.yaml`).

## Request: `POST /v1/events`

Headers:

```
Content-Type: application/json
Authorization: Bearer <token>
```

The `Authorization` header is **required**. The token is looked up in the
agent's `devices.yaml` and is the primary authentication credential — see
"Auth and device resolution" below.

Body fields:

| Field              | Type   | Required | Notes |
|--------------------|--------|----------|-------|
| `protocol_version` | int    | no (default `1`) | Reserved for future breaking changes. Omission is always safe. |
| `device_id`        | string | **yes** | Identifies the physical kiosk/gate machine. No safe default exists — a missing value would silently corrupt attribution, so this is rejected rather than defaulted. |
| `event_id`         | string | no, but recommended | Client-generated unique id (e.g. a UUID) used for idempotent retries. If omitted, the agent generates one server-side and that particular call loses dedupe protection. |
| `occurred_at`      | string (RFC3339) | no | Client-observed timestamp. Untrusted (client clocks can be wrong or unset). If omitted or unparseable, the agent falls back to its own receive time — never rejected for a bad client clock. |
| `event`            | string | yes | Symbolic event name, e.g. `PAYMENT_TIMEOUT`. Unchanged from the original single-gate protocol. At most 128 characters; longer names are cut to 128 (the agent learns each new name for good, so names must not contain changing values such as ids). Once a code group has used numbers 1–898, further new names share `<CODE>-899` instead of being learned. |
| `category`         | string | no | Hint used only if `event` is not yet in the agent's registry. Either a category name (`SYSTEM`) or its code prefix (`SYS`), case-insensitive; the new event is numbered under the registry's prefix for that category (`SYSTEM` → `SYS-00N`). Missing or unknown values fall back to `APP`, and an unknown value is kept in `context.category_hint`. |
| `severity`         | string | no | Overrides the registry's default severity for this event: `INFO`, `LOW`, `MEDIUM`, `HIGH` or `CRITICAL`, case-insensitive. Logging level names are mapped (`DEBUG` → `INFO`, `WARNING`/`WARN` → `MEDIUM`, `ERROR` → `HIGH`, `FATAL` → `CRITICAL`). Any other value keeps the registry default and is stored in `context.severity_hint`. |
| `message`          | string | no | Free-form human-readable detail. |
| `device`           | string | no | The **sub-device at the gate** (e.g. `READER01`, `PRINTER01`) — unrelated to `device_id`, which identifies the kiosk machine itself. Do not confuse the two. |
| `app_version`      | string | no | |
| `transaction_ref`  | string | no | Reference id only — never transaction/payment data. |
| `retry_count`      | int    | no | |
| `endpoint`         | string | no | |
| `http_status`      | int    | no | |
| `duration_ms`      | int    | no | |
| `context`          | object | no | Free-form extra key/value context. |
| `stack`            | string | no | A formatted traceback: file, function and line per frame, innermost last — **never local variable values**. Stored as sent (cut at 16 KiB) and shown on the dashboard's event detail. The Python SDK fills it from `client.exception()`. |

**Catch-all log events.** An event whose name starts with `LOG_` (the kiosk
logging handler forwarding every `logger.warning`/`error`) is kept but, when
its severity resolves to MEDIUM, demoted to **LOW** so it never colours a
gate: in the field these are configuration notes (MQTT off, LPR not sending,
QRIS secret missing), not faults. `ERROR`-level ones stay HIGH so a code
failure is still visible. Name the recurring ones as registry events instead.

## Enrollment: `POST /v1/enroll`

A kiosk can obtain its own device token from the site's agent instead of
an operator running `device add`. The only credential is the **site enroll
key** (`site.enroll_key` / `SENTINEL_SITE_KEY` on the agent; one per
location, shared by its gates). It is used for this call alone; from then
on the kiosk authenticates with the returned token. No `Authorization`
header.

```json
{
  "site_key":   "<site enroll key>",
  "device_id":  "PCC-G01-POS01",
  "gate":       "G01",
  "pos":        "POS01",
  "gate_name":  "Gate Keluar Utara",
  "platform":   "tongbao",
  "app_version":"3.38.0"
}
```

| Field | Required | Notes |
|---|---|---|
| `site_key`  | yes | Compared in constant time. Wrong keys count against a per-IP limiter (**429** after repeated failures, same policy as dashboard login). |
| `device_id` | yes | 1–64 chars: letters, digits, `.` `_` `-`. Should be unique per kiosk; the app's existing `SENTINEL_DEVICE_ID`. |
| `gate`      | yes | Short code (`G01`), upper-cased by the agent. |
| `pos`       | no  | Same charset as `device_id`. |
| `gate_name` | no  | Human label shown on the card (≤ 64 chars). |
| `platform`, `app_version` | no | Stored as the first heartbeat so the card is complete immediately. |

Response **201**:

```json
{"device_id":"PCC-G01-POS01","location":"PCC","gate":"G01","pos":"POS01",
 "site_name":"Plaza Central City","token":"<48-char token>"}
```

`location` is always the agent's `site.code` — a kiosk cannot claim another
site. The token is returned **once** and only its SHA-256 is stored
(`devices` table); the kiosk must persist it. Re-enrolling an existing
enrolled `device_id` **rotates** its token (the old one stops working at
once), which is how a reinstalled kiosk recovers without an operator. A
`device_id` that exists in `devices.yaml` cannot be taken over → **409**.

| Status | Meaning |
|---|---|
| `201` | Enrolled (or re-enrolled). |
| `400` | Bad JSON or an invalid `device_id`/`gate`/`pos`. |
| `401` | Wrong site key. |
| `404` | Enrollment is off on this agent (`site.code` or `site.enroll_key` unset). |
| `409` | `device_id` belongs to a static `devices.yaml` entry. |
| `429` | Too many wrong keys from this IP. |

Operators see enrolled devices with `sentinel-agent device list`
(column `enrolled`) and remove one with `device revoke -id …`.

## Auth and device resolution

Every entry in the agent's `devices.yaml`, and every enrolled device,
binds exactly one `device_id` to exactly one bearer token. Resolution
proceeds:

1. No valid token presented → **401**. This is a pure auth failure: with no
   trusted credential at all, the event cannot be safely attributed to any
   device, mapped or not, so it is rejected outright.
2. Valid token, and the payload's `device_id` matches the token's bound
   `device_id` → normal path. Location/gate/pos are stamped from the
   `devices.yaml` entry.
3. Valid token, but the payload's `device_id` does **not** match the
   token's bound `device_id` → **UNMAPPED path**. The event is still
   accepted (never silently dropped), tagged `location="UNMAPPED"`,
   `gate=<the payload's claimed device_id>`, `pos=""`, and a warning is
   logged agent-side. This covers the realistic failure mode: a
   misconfigured SDK, a token copy-pasted to the wrong kiosk, or a
   `device_id` renamed on one side but not the other — it stays visible
   (e.g. via `GET /v1/summary` or raw event dumps) instead of vanishing.

There is deliberately no "unknown device_id, no token" acceptance path:
since every valid token already implies a known device, the only way to
reach the UNMAPPED state is with a *valid* credential whose claimed
identity doesn't match.

## Response codes

| Status | Meaning |
|--------|---------|
| `201`  | New event stored. Body is the stored record. |
| `200`  | Idempotent replay — `event_id` was already seen. Body is the **original** stored record, no new row created. |
| `400`  | Malformed JSON, a required field (`event`, `device_id`) is missing/blank, or the body exceeds 1 MiB. |
| `401`  | Missing/malformed `Authorization` header, or unknown token. (A mismatched `device_id` is **not** a 401 — see the UNMAPPED path above.) |
| `500`  | Storage failure. |

There is intentionally no `403`/`404` for an unmapped device — see above.

## Compatibility rule

Fields may be **added** in future protocol versions. Existing fields are
**never removed or renamed**. Servers ignore unknown request fields
(already true today — Go's `json.Decoder` does this by default). Clients
should ignore unknown response fields.

## Timestamps

Two timestamps are stored per event:

- `occurred_at` — client-observed, **untrusted** (may be skewed or absent).
- `received_at` — server-observed, authoritative for ordering and
  `/v1/summary` window queries.

## Batch: `POST /v1/events/batch`

Same headers and auth as `POST /v1/events`. Used by SDKs to drain a
backlog after the agent was unreachable.

```json
{"events": [ <event>, <event>, ... ]}
```

- 1–100 events per request; otherwise **400** and nothing is stored.
- Each event follows the single-event rules above, independently. The
  response is **200** whenever the batch itself was readable, with one
  result per input, in order:

```json
{"results": [
  {"index": 0, "event_id": "…", "status": "created",   "error_code": "STI-G03-PAY-002"},
  {"index": 1, "event_id": "…", "status": "duplicate", "error_code": "STI-G03-PAY-002"},
  {"index": 2, "event_id": "…", "status": "error", "error": "field 'event' is required"},
  {"index": 3, "event_id": "…", "status": "error", "error": "failed to store event: …", "retryable": true}
]}
```

A client may forget an event once its result is `created` or
`duplicate`, or `error` without `retryable` (the same payload would fail
again). It should retry only `retryable` errors. An agent without this
endpoint answers **404**; the client then falls back to single posts.

## Heartbeat: `POST /v1/heartbeat`

Same headers and auth. The SDK sends this at most once a minute, **from the
host application's own main loop** (`client.beat()`), so a hung app stops
heartbeating and shows up as STALE/DOWN.

| Field | Type | Notes |
|-------|------|-------|
| `device_id`      | string | Cross-checked only; the token decides which device this is. A mismatch is logged, nothing is tagged. |
| `app_version`    | string | |
| `sdk_version`    | string | |
| `uptime_s`       | int    | Seconds since the app started. |
| `outbox_pending` | int    | Events queued on the kiosk, not yet delivered. A non-zero value colours the gate yellow. |
| `outbox_dropped` | int    | Events the SDK discarded since it started (queue/outbox full). |
| `sent_at`        | string (RFC3339) | Client clock; informational. |
| `platform`       | string | Product variant/OS: `tongbao` (MQTT + hardware, Lubuntu ARM), `windows`, `linux`. Lower-cased by the agent, kept until the next value. Shown on the gate card so MQTT events are read in context. |
| `gate_name`      | string | Human label of the gate as set in the kiosk app's settings (≤ 64 chars). Updates the card/detail title for any device. |
| `gate`, `pos`    | string | Identity the kiosk claims for itself. For an **enrolled** device a change moves it (its future events get the new gate/pos; logged as "moved itself"). For a static `devices.yaml` device these are ignored — gate is the operator's decision. |

Response **200** `{"status": "ok", "server_time": "<RFC3339>"}`. Heartbeats
are not stored as events; they update the device's row in `device_state`.
Because `gate_name`/`gate`/`pos` ride on the heartbeat, renaming a gate in
the kiosk app reaches the dashboard within a minute, with no agent restart.

### Liveness and gate colour

A device's *last seen* is its latest heartbeat or event (UNMAPPED events
don't count). With the default thresholds (`status:` in config.yaml):

| Status | Last seen |
|--------|-----------|
| `ONLINE` | ≤ 3 minutes ago |
| `STALE`  | ≤ 10 minutes ago |
| `DOWN`   | longer |
| `NEVER`  | listed in devices.yaml, never heard from |

Gate colour: **red** if DOWN/NEVER or a HIGH/CRITICAL event in the last 10
minutes; **yellow** if STALE, a MEDIUM event in the last 10 minutes, or
`outbox_pending > 0`; otherwise **green**. INFO and LOW never colour a gate.

### Status history

Every 30 s the agent records liveness changes in `status_history`. Each
fleet/device row carries `status_since` (RFC3339, when the current status
began) and `GET /v1/devices/{id}` adds `status_history` (last 20 changes,
newest first), which is what "Mati sejak 10:58" and the gate's "Riwayat
status" table are built from. For one stale period after the agent starts
only ONLINE is recorded, since every kiosk looks DOWN until it heartbeats.
`NEVER` is not recorded.

## Read API (dashboard)

`GET /v1/fleet`, `GET /v1/devices/{device_id}`, `GET /v1/events`,
`GET /v1/summary` and `GET /v1/auth/me` require a dashboard login: the
session cookie from `POST /v1/auth/login`, or HTTP Basic credentials of a
`users.yaml` user (for scripts). **Device tokens are rejected here** — a
kiosk's credential is write-only and must not read other kiosks' data.
`GET /v1/health` stays public and reveals no event or device data.

`GET /v1/fleet` carries, besides `devices`, `unmapped_24h` and `generated_at`:

- `site`: `{code, name, enroll_enabled}` from config.yaml.
- `agent`: `{version, listen_addr, tailscale_ip, db_size_bytes,
  db_max_bytes, disk_free_bytes, retention_days}`. `tailscale_ip` is the
  host's first IPv4 in 100.64.0.0/10 (cached a minute). The dashboard
  warns when the DB is above 80 % of `retention.max_db_mb` or the disk
  has under 1 GiB free.

Each device row includes `name` (the kiosk's `gate_name`), `platform`,
`status_since` and `enrolled` (true when the token came from `/v1/enroll`).

`GET /v1/events` filters: `device_id`, `location`, `gate`, `error_code`,
`event`, `severity` (minimum: `MEDIUM` also returns HIGH and CRITICAL),
`since`/`until` (RFC3339, on `received_at`), `limit` (≤ 1000, default 100)
and `before_id` for paging back (newest first).
