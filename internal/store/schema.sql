CREATE TABLE IF NOT EXISTS events (
  event_id     TEXT PRIMARY KEY,
  captured_at  TIMESTAMP NOT NULL,
  endpoint_type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  synced_at    TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_events_synced   ON events(synced_at);
CREATE INDEX IF NOT EXISTS idx_events_captured ON events(captured_at);
