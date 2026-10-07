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
| `event`            | string | yes | Symbolic event name, e.g. `PAYMENT_TIMEOUT`. Unchanged from the original single-gate protocol. |
| `category`         | string | no | Hint used only if `event` is not yet in the agent's registry. |
| `severity`         | string | no | Overrides the registry's default severity for this event. |
| `message`          | string | no | Free-form human-readable detail. |
| `device`           | string | no | The **sub-device at the gate** (e.g. `READER01`, `PRINTER01`) — unrelated to `device_id`, which identifies the kiosk machine itself. Do not confuse the two. |
| `app_version`      | string | no | |
| `transaction_ref`  | string | no | Reference id only — never transaction/payment data. |
| `retry_count`      | int    | no | |
| `endpoint`         | string | no | |
| `http_status`      | int    | no | |
| `duration_ms`      | int    | no | |
| `context`          | object | no | Free-form extra key/value context. |

## Auth and device resolution

Every entry in the agent's `devices.yaml` binds exactly one `device_id` to
exactly one bearer token. Resolution proceeds:

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
| `400`  | Malformed JSON, or a required field (`event`, `device_id`) is missing/blank. |
| `401`  | Missing/malformed `Authorization` header, unknown token, or (see above) a mismatched device_id. |
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

## Reserved, not yet implemented

A heartbeat/device-inventory endpoint (app version, OS, current state,
online/stale/down derivation) is reserved under protocol v1 but is **not**
implemented in this phase (Tahap 1). Its exact shape is intentionally left
unspecified here to avoid committing to a design prematurely.
