CREATE TABLE domain_listing_snapshots (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, domain TEXT NOT NULL,
 provider_slug TEXT NOT NULL, connection_id INTEGER NOT NULL, state TEXT NOT NULL,
 provider_status TEXT NOT NULL DEFAULT '', epoch INTEGER NOT NULL DEFAULT 1,
 snapshot_json TEXT NOT NULL DEFAULT '{}', fingerprint TEXT NOT NULL DEFAULT '',
 checked_at TEXT NOT NULL DEFAULT '', next_check_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', failure_count INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(project_id,domain,connection_id)
);
CREATE INDEX domain_listing_due ON domain_listing_snapshots(project_id,next_check_at);
CREATE TABLE domain_listing_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT, listing_id INTEGER NOT NULL, project_id TEXT NOT NULL,
 domain TEXT NOT NULL, state TEXT NOT NULL, provider_status TEXT NOT NULL,
 source TEXT NOT NULL, snapshot_json TEXT NOT NULL, observed_at TEXT NOT NULL
);
CREATE INDEX domain_listing_history_domain ON domain_listing_history(project_id,domain,id);
CREATE TABLE domain_sales (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, listing_id INTEGER NOT NULL,
 domain TEXT NOT NULL, provider_slug TEXT NOT NULL, connection_id INTEGER NOT NULL,
 epoch INTEGER NOT NULL, sale_key TEXT NOT NULL, sold_at TEXT NOT NULL DEFAULT '',
 sale_price TEXT NOT NULL DEFAULT '', payout TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '',
 evidence_json TEXT NOT NULL, confirmed_at TEXT NOT NULL,
 UNIQUE(listing_id,epoch,sale_key)
);
CREATE TABLE domain_sync_cursors (
 project_id TEXT NOT NULL, connection_id INTEGER NOT NULL, kind TEXT NOT NULL,
 cursor TEXT NOT NULL DEFAULT '', window_from TEXT NOT NULL DEFAULT '', window_to TEXT NOT NULL DEFAULT '',
 completed_at TEXT NOT NULL DEFAULT '', next_check_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(project_id,connection_id,kind)
);
CREATE TABLE domain_lifecycle_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, project_id TEXT NOT NULL,
 domain TEXT NOT NULL, topic TEXT NOT NULL, dedupe_key TEXT NOT NULL, payload_json TEXT NOT NULL,
 delivery_status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
 delivery_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, delivered_at TEXT NOT NULL DEFAULT '',
 UNIQUE(project_id,dedupe_key)
);
CREATE INDEX domain_lifecycle_events_pending ON domain_lifecycle_events(project_id,delivery_status,id);
