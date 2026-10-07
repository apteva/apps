-- Observational data only: raw addresses are not stored in public call diagnostics.
CREATE TABLE IF NOT EXISTS telephony_browser_network_events (
  id TEXT PRIMARY KEY,
  call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  event_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_browser_network_call ON telephony_browser_network_events(project_id,call_id,occurred_at);
CREATE INDEX IF NOT EXISTS idx_browser_network_expiry ON telephony_browser_network_events(expires_at);
