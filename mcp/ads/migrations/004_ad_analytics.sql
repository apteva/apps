-- Normalized reporting cache. Provider objects remain authoritative; these
-- rows make bounded analytics fast, comparable, and available between syncs.

CREATE TABLE IF NOT EXISTS ad_entities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    platform TEXT NOT NULL,
    level TEXT NOT NULL,
    native_entity_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    campaign_id TEXT NOT NULL DEFAULT '',
    ad_group_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    provider_data_json TEXT NOT NULL DEFAULT '{}',
    last_seen_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ad_entities_native
    ON ad_entities(project_id, ad_account_id, level, native_entity_id);
CREATE INDEX IF NOT EXISTS idx_ad_entities_account
    ON ad_entities(project_id, ad_account_id, level, last_seen_at);

CREATE TABLE IF NOT EXISTS ad_metric_points (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    platform TEXT NOT NULL,
    level TEXT NOT NULL,
    native_entity_id TEXT NOT NULL,
    entity_name TEXT NOT NULL DEFAULT '',
    campaign_id TEXT NOT NULL DEFAULT '',
    campaign_name TEXT NOT NULL DEFAULT '',
    ad_group_id TEXT NOT NULL DEFAULT '',
    ad_group_name TEXT NOT NULL DEFAULT '',
    point_date TEXT NOT NULL,
    currency TEXT NOT NULL DEFAULT '',
    timezone_name TEXT NOT NULL DEFAULT '',
    spend_micros INTEGER NOT NULL DEFAULT 0,
    impressions INTEGER NOT NULL DEFAULT 0,
    reach INTEGER NOT NULL DEFAULT 0,
    clicks INTEGER NOT NULL DEFAULT 0,
    link_clicks INTEGER NOT NULL DEFAULT 0,
    conversions REAL NOT NULL DEFAULT 0,
    conversion_value_micros INTEGER NOT NULL DEFAULT 0,
    video_views INTEGER NOT NULL DEFAULT 0,
    provider_metrics_json TEXT NOT NULL DEFAULT '{}',
    fetched_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ad_metric_points_unique
    ON ad_metric_points(project_id, ad_account_id, level, native_entity_id, point_date);
CREATE INDEX IF NOT EXISTS idx_ad_metric_points_range
    ON ad_metric_points(project_id, ad_account_id, level, point_date);
CREATE INDEX IF NOT EXISTS idx_ad_metric_points_campaign
    ON ad_metric_points(project_id, ad_account_id, campaign_id, point_date);

CREATE TABLE IF NOT EXISTS ad_sync_state (
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    level TEXT NOT NULL,
    last_incremental_at TEXT NOT NULL DEFAULT '',
    last_reconciled_at TEXT NOT NULL DEFAULT '',
    last_date_from TEXT NOT NULL DEFAULT '',
    last_date_to TEXT NOT NULL DEFAULT '',
    last_status TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(project_id, ad_account_id, level)
);
