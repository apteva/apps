CREATE TABLE graphql_security (
  project_id TEXT NOT NULL,
  api_slug TEXT NOT NULL,
  policy_json TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(project_id, api_slug)
);
