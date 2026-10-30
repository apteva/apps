-- Paid supporter observations per account (YouTube channel members, later
-- patrons or subscribers). total is NULL unless state='available': an
-- unknown count is never stored as zero. Counts only, no member identities.
CREATE TABLE IF NOT EXISTS social_supporter_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  social_account_id INTEGER NOT NULL,
  observed_at TEXT NOT NULL,
  state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  total INTEGER,
  provider_total INTEGER,
  complete INTEGER NOT NULL DEFAULT 0,
  levels_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_social_supporter_snapshots_account ON social_supporter_snapshots(project_id, social_account_id, id);
