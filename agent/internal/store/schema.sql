CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id       TEXT NOT NULL DEFAULT '',
    event_id        TEXT,
    occurred_at     TEXT,
    location        TEXT NOT NULL,
    gate            TEXT NOT NULL,
    pos             TEXT,
    device          TEXT,
    category        TEXT NOT NULL,
    error_code      TEXT NOT NULL,
    event_name      TEXT NOT NULL,
    severity        TEXT NOT NULL,
    message         TEXT,
    transaction_ref TEXT,
    retry_count     INTEGER,
    endpoint        TEXT,
    http_status     INTEGER,
    duration_ms     INTEGER,
    context_json    TEXT,
    app_version     TEXT,
    received_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_error_code   ON events(error_code);
CREATE INDEX IF NOT EXISTS idx_events_received_at  ON events(received_at);
CREATE INDEX IF NOT EXISTS idx_events_event_name   ON events(event_name);
CREATE INDEX IF NOT EXISTS idx_events_device_id    ON events(device_id);

-- Partial index: allows blank/server-generated-fallback event_ids to
-- coexist without fighting each other for uniqueness, while still
-- guaranteeing a real client-supplied event_id is never stored twice.
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_event_id ON events(event_id)
    WHERE event_id IS NOT NULL AND event_id != '';

CREATE TABLE IF NOT EXISTS event_registry (
    event_name TEXT PRIMARY KEY,
    category   TEXT NOT NULL,
    code       TEXT NOT NULL,
    number     INTEGER NOT NULL,
    severity   TEXT NOT NULL
);

-- Fleet view and retention filter by device over time.
CREATE INDEX IF NOT EXISTS idx_events_device_received ON events(device_id, received_at);

-- Latest known state per device (heartbeats + last event time). Kept out
-- of `events` so a 60s heartbeat from every kiosk doesn't bloat it.
CREATE TABLE IF NOT EXISTS device_state (
    device_id         TEXT PRIMARY KEY,
    last_heartbeat_at TEXT,
    last_event_at     TEXT,
    app_version       TEXT,
    sdk_version       TEXT,
    uptime_s          INTEGER NOT NULL DEFAULT 0,
    outbox_pending    INTEGER NOT NULL DEFAULT 0,
    outbox_dropped    INTEGER NOT NULL DEFAULT 0,
    remote_addr       TEXT
);

-- Every alert actually sent, used for cooldowns and as an audit trail.
CREATE TABLE IF NOT EXISTS alert_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_key  TEXT NOT NULL,  -- e.g. "event:STI-G03-POS01:STI-G03-PAY-002", "status:STI-G03-POS01"
    route      TEXT NOT NULL,
    message    TEXT NOT NULL,
    sent_at    TEXT NOT NULL,
    ok         INTEGER NOT NULL,
    error      TEXT
);
CREATE INDEX IF NOT EXISTS idx_alert_log_key ON alert_log(alert_key, route, sent_at);

-- Small key/value store for agent-internal bookkeeping (e.g. the alerter's
-- last processed event id, last known device statuses).
CREATE TABLE IF NOT EXISTS agent_kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
