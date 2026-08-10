CREATE TABLE IF NOT EXISTS ad_audience_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    audience_resource_id INTEGER,
    native_audience_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('add', 'remove', 'replace')),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('storage', 'crm_segment')),
    source_ref TEXT NOT NULL,
    mapping_json TEXT NOT NULL DEFAULT '{}',
    consent_json TEXT NOT NULL DEFAULT '{}',
    idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'processing', 'provider_processing', 'completed', 'failed')),
    total_rows INTEGER NOT NULL DEFAULT 0,
    processed_rows INTEGER NOT NULL DEFAULT 0,
    accepted_rows INTEGER NOT NULL DEFAULT 0,
    rejected_rows INTEGER NOT NULL DEFAULT 0,
    source_checksum TEXT NOT NULL DEFAULT '',
    provider_request_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TEXT NOT NULL DEFAULT (datetime('now')),
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (ad_account_id) REFERENCES ad_accounts(id) ON DELETE CASCADE,
    FOREIGN KEY (audience_resource_id) REFERENCES ad_resources(id) ON DELETE SET NULL,
    UNIQUE(project_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_ad_audience_jobs_ready
    ON ad_audience_jobs(status, available_at, id);

CREATE INDEX IF NOT EXISTS idx_ad_audience_jobs_scope
    ON ad_audience_jobs(project_id, ad_account_id, id DESC);
