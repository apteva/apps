CREATE TABLE IF NOT EXISTS domain_valuations (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, domain TEXT NOT NULL,
 provider_slug TEXT NOT NULL, status TEXT NOT NULL, auction REAL, marketplace REAL, brokerage REAL,
 prediction_id TEXT, raw_json TEXT NOT NULL DEFAULT '{}', error_message TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_domain_valuations_project_domain ON domain_valuations(project_id, domain, created_at DESC);
