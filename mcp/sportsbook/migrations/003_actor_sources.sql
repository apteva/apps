CREATE TABLE actor_sport_sources (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  sport TEXT NOT NULL,
  competition_id TEXT NOT NULL DEFAULT '',
  actor_id INTEGER NOT NULL CHECK(actor_id>0),
  operation TEXT NOT NULL,
  input_json TEXT NOT NULL DEFAULT '{}',
  field_map_json TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  read_only INTEGER NOT NULL DEFAULT 1 CHECK(read_only=1),
  created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(project_id,sport) REFERENCES sports(project_id,id)
  -- An empty competition_id means any competition in the configured sport.
  -- Validation in the source configuration handler enforces non-empty IDs.
);
CREATE INDEX actor_sport_sources_lookup ON actor_sport_sources(project_id,sport,competition_id,enabled);
