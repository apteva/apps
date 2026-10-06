ALTER TABLE graphql_request_logs ADD COLUMN runtime_metrics_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_graphql_logs_queue ON graphql_request_logs(project_id, COALESCE(json_extract(runtime_metrics_json, '$.queue_ms'), 0));
CREATE INDEX IF NOT EXISTS idx_graphql_logs_coalesced ON graphql_request_logs(project_id, COALESCE(json_extract(runtime_metrics_json, '$.coalesced'), 0));
