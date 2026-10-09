-- No spend/impression columns: event segmentation cannot duplicate delivery.
CREATE TABLE IF NOT EXISTS ad_conversion_points (
 project_id TEXT NOT NULL, ad_account_id INTEGER NOT NULL, level TEXT NOT NULL,
 native_entity_id TEXT NOT NULL, point_date TEXT NOT NULL, event_id TEXT NOT NULL,
 event_name TEXT NOT NULL, measurement_source TEXT NOT NULL,
 attribution_window TEXT NOT NULL DEFAULT 'provider_default',
 conversions REAL NOT NULL, value_micros INTEGER, currency TEXT NOT NULL DEFAULT '',
 fetched_at TEXT NOT NULL,
 PRIMARY KEY(project_id,ad_account_id,level,native_entity_id,point_date,event_id,measurement_source,attribution_window)
);
CREATE INDEX IF NOT EXISTS ad_conversion_points_range ON ad_conversion_points(project_id,ad_account_id,level,point_date);
CREATE TABLE IF NOT EXISTS ad_create_requests (
 project_id TEXT NOT NULL, ad_account_id INTEGER NOT NULL, request_key TEXT NOT NULL,
 request_hash TEXT NOT NULL, status TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(project_id,ad_account_id,request_key)
);
-- Keep bindings separate from provider snapshots, which refreshes can replace.
CREATE TABLE IF NOT EXISTS ad_mobile_campaigns (
 project_id TEXT NOT NULL, ad_account_id INTEGER NOT NULL, campaign_id TEXT NOT NULL,
 mobile_app_resource_id INTEGER NOT NULL, measurement_source_resource_id INTEGER NOT NULL,
 conversion_event_resource_id INTEGER NOT NULL DEFAULT 0, app_goal TEXT NOT NULL,
 PRIMARY KEY(project_id,ad_account_id,campaign_id)
);
-- Event-sync health is independent of delivery-sync health.
CREATE TABLE IF NOT EXISTS ad_conversion_sync_state (
 project_id TEXT NOT NULL,ad_account_id INTEGER NOT NULL,level TEXT NOT NULL,
 last_success_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',
 failure_count INTEGER NOT NULL DEFAULT 0,next_attempt_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_id,ad_account_id,level)
);
