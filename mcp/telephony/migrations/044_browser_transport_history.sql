-- Bounded observational history, excluded from call list/SSE projections.
CREATE TABLE IF NOT EXISTS telephony_browser_transport_samples (
 id TEXT PRIMARY KEY,
 call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 sample_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_browser_transport_call ON telephony_browser_transport_samples(project_id,call_id,occurred_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_browser_transport_kind ON telephony_browser_transport_samples(project_id,call_id,kind,occurred_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_browser_transport_expiry ON telephony_browser_transport_samples(expires_at);
