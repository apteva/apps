ALTER TABLE graphql_request_logs ADD COLUMN environment TEXT NOT NULL DEFAULT '';
ALTER TABLE graphql_request_logs ADD COLUMN errors_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE graphql_request_logs ADD COLUMN timings_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_graphql_logs_time ON graphql_request_logs(project_id, julianday(created_at), id DESC);
CREATE INDEX IF NOT EXISTS idx_graphql_logs_environment_time ON graphql_request_logs(project_id, environment, julianday(created_at));
