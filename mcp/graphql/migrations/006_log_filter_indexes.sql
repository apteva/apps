CREATE INDEX IF NOT EXISTS idx_graphql_logs_project_duration
  ON graphql_request_logs(project_id, duration_ms DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_graphql_logs_project_rows
  ON graphql_request_logs(project_id, row_count DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_graphql_logs_project_resolvers
  ON graphql_request_logs(project_id, resolver_count DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_graphql_logs_project_response
  ON graphql_request_logs(project_id, response_bytes DESC, id DESC);
