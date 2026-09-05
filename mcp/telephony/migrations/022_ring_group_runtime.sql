-- Persist the order, active offers, and winner independently of provider callbacks.
CREATE TABLE IF NOT EXISTS call_ring_runs (
  id TEXT PRIMARY KEY,
  call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  ring_group_id TEXT NOT NULL,
  strategy TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'ringing',
  overflow_node_id TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  deadline_at TEXT NOT NULL,
  UNIQUE(call_id,node_id)
);
CREATE INDEX IF NOT EXISTS idx_call_ring_runs_pending ON call_ring_runs(project_id,status,deadline_at);
CREATE TABLE IF NOT EXISTS ring_group_cursors (
  project_id TEXT NOT NULL,
  ring_group_id TEXT NOT NULL,
  next_position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id,ring_group_id)
);
ALTER TABLE call_offers ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
ALTER TABLE call_offers ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE call_offers ADD COLUMN timeout_sec INTEGER NOT NULL DEFAULT 20;
ALTER TABLE call_offers ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN agent_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE call_offers ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE call_offers ADD COLUMN destination_name TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN delivered_at TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN delivery_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE call_offers ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_call_offers_run ON call_offers(run_id,status,position);
CREATE INDEX IF NOT EXISTS idx_call_offers_agent ON call_offers(project_id,agent_id,status,expires_at);
ALTER TABLE call_legs ADD COLUMN offer_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_call_legs_offer ON call_legs(offer_id) WHERE offer_id<>'';
