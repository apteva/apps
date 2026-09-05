-- Persistent work survives API deletion and process restarts.
CREATE TABLE api_policy_sync (
  api_id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  delete_requested INTEGER NOT NULL DEFAULT 0,
  pending INTEGER NOT NULL DEFAULT 1,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE api_exposures (
  hostname TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  api_id INTEGER NOT NULL,
  cleanup INTEGER NOT NULL DEFAULT 0,
  dns_zone TEXT NOT NULL DEFAULT '',
  dns_name TEXT NOT NULL DEFAULT '',
  dns_type TEXT NOT NULL DEFAULT '',
  dns_value TEXT NOT NULL DEFAULT '',
  dns_record_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ix_api_logs_retention ON api_request_logs(created_at,id);
CREATE INDEX ix_api_exposures_api ON api_exposures(api_id);
CREATE INDEX ix_api_logs_page ON api_request_logs(project_id,api_id,id DESC);
-- v0.5.0 did not cascade request-log deletion.
DELETE FROM api_request_logs WHERE api_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM apis WHERE apis.id=api_request_logs.api_id);
-- Persist future cleanup even for hostnames created before this migration.
INSERT OR IGNORE INTO api_exposures(hostname,project_id,api_id)
 SELECT hostname,project_id,id FROM apis WHERE hostname<>'';
UPDATE api_routes SET path_pattern='/' WHERE path_pattern='';
