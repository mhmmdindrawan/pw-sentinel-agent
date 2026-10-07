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
