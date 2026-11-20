-- Intelligence world model. Three layers stay separate:
--   observed  — sources, ingest runs, evidence (003), claims
--   estimated — entities (001, extended), relationships, state values
--   futures   — questions, prices, forecasts, resolutions, scenarios
-- Forecast rows are immutable: a new belief is a new revision row.

-- Server-assigned knowledge time. observed_at may be supplied by a caller
-- (historical imports); ingested_at never is, so forecasts built on
-- ingested_at <= cutoff cannot see the future.
ALTER TABLE intelligence_evidence ADD COLUMN ingested_at TIMESTAMP;
-- Normalized-title hash shared by syndicated copies of one story, so
-- repeated reporting is not counted as independent confirmation.
ALTER TABLE intelligence_evidence ADD COLUMN origin_key TEXT NOT NULL DEFAULT '';
UPDATE intelligence_evidence SET ingested_at = COALESCE(created_at, observed_at) WHERE ingested_at IS NULL;
CREATE INDEX ix_intel_evidence_ingested ON intelligence_evidence(project_id, ingested_at DESC);
CREATE INDEX ix_intel_evidence_origin ON intelligence_evidence(project_id, origin_key);

-- Connector registry: what is collected, under which permitted use, and how
-- fresh it is.
CREATE TABLE intel_sources (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  slug            TEXT NOT NULL,
  connector       TEXT NOT NULL,
  enabled         INTEGER NOT NULL DEFAULT 1,
  interval_sec    INTEGER NOT NULL DEFAULT 900,
  config          TEXT NOT NULL DEFAULT '{}',
  permitted_use   TEXT NOT NULL DEFAULT '',
  licence         TEXT NOT NULL DEFAULT '',
  retention_days  INTEGER NOT NULL DEFAULT 0,
  cursor          TEXT NOT NULL DEFAULT '',
  last_run_at     TIMESTAMP,
  last_success_at TIMESTAMP,
  last_error      TEXT NOT NULL DEFAULT '',
  items_total     INTEGER NOT NULL DEFAULT 0,
  calls_total     INTEGER NOT NULL DEFAULT 0,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_intel_sources ON intel_sources(project_id, slug);

CREATE TABLE intel_ingest_runs (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT NOT NULL,
  source_slug TEXT NOT NULL,
  started_at  TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  status      TEXT NOT NULL DEFAULT 'running',
  fetched     INTEGER NOT NULL DEFAULT 0,
  inserted    INTEGER NOT NULL DEFAULT 0,
  duplicates  INTEGER NOT NULL DEFAULT 0,
  calls       INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ix_intel_runs ON intel_ingest_runs(project_id, source_slug, started_at DESC);

-- Statements extracted from evidence. Proposed claims are not world state;
-- acceptance is a separate, recorded step. Conflicting claims coexist.
CREATE TABLE intel_claims (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  evidence_id       INTEGER NOT NULL,
  subject_entity_id INTEGER,
  subject           TEXT NOT NULL,
  predicate         TEXT NOT NULL,
  object_entity_id  INTEGER,
  object            TEXT NOT NULL DEFAULT '',
  value             REAL,
  unit              TEXT NOT NULL DEFAULT '',
  claim_time        TIMESTAMP,
  confidence        REAL,
  extractor         TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'proposed',
  created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_intel_claims_evidence ON intel_claims(project_id, evidence_id);
CREATE INDEX ix_intel_claims_subject ON intel_claims(project_id, subject_entity_id);

-- Entities become typed. Persons are not a permitted kind: people appear
-- only as public roles (kind public_role) and are never profiled.
ALTER TABLE entities ADD COLUMN kind TEXT NOT NULL DEFAULT 'concept';
ALTER TABLE entities ADD COLUMN description TEXT NOT NULL DEFAULT '';

CREATE TABLE intel_relationships (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT NOT NULL,
  from_entity_id INTEGER NOT NULL,
  rel_type       TEXT NOT NULL,
  to_entity_id   INTEGER NOT NULL,
  status         TEXT NOT NULL,           -- observed | reported | inferred | assumed
  confidence     REAL,
  valid_from     TIMESTAMP,
  valid_to       TIMESTAMP,
  evidence_ids   TEXT NOT NULL DEFAULT '[]',
  claim_ids      TEXT NOT NULL DEFAULT '[]',
  note           TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  retracted_at   TIMESTAMP
);
CREATE INDEX ix_intel_rel_from ON intel_relationships(project_id, from_entity_id);
CREATE INDEX ix_intel_rel_to ON intel_relationships(project_id, to_entity_id);

CREATE TABLE intel_state_values (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT NOT NULL,
  entity_id   INTEGER NOT NULL,
  variable    TEXT NOT NULL,
  value       REAL NOT NULL,
  unit        TEXT NOT NULL DEFAULT '',
  valid_at    TIMESTAMP NOT NULL,
  evidence_id INTEGER,
  source      TEXT NOT NULL,
  ingested_at TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX ux_intel_state ON intel_state_values(project_id, entity_id, variable, valid_at, source);

-- Resolvable questions. Rules are fixed at creation; a question is never
-- edited after its first forecast (void it and create a new one instead).
CREATE TABLE intel_questions (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  origin            TEXT NOT NULL,        -- market | manual
  venue             TEXT NOT NULL DEFAULT '',
  external_id       TEXT NOT NULL DEFAULT '',
  url               TEXT NOT NULL DEFAULT '',
  text              TEXT NOT NULL,
  resolution_rule   TEXT NOT NULL DEFAULT '',
  resolution_source TEXT NOT NULL DEFAULT '',
  category          TEXT NOT NULL DEFAULT '',
  event_key         TEXT NOT NULL DEFAULT '',     -- venue event grouping (caps questions per event)
  template          TEXT NOT NULL DEFAULT '',     -- number-collapsed venue question (caps repeated props)
  topics            TEXT NOT NULL DEFAULT '[]',
  entity_ids        TEXT NOT NULL DEFAULT '[]',
  deadline          TIMESTAMP NOT NULL,
  status            TEXT NOT NULL DEFAULT 'open',  -- open | resolved | void
  outcome           INTEGER,                       -- 1 yes, 0 no
  resolved_at       TIMESTAMP,
  resolution_note   TEXT NOT NULL DEFAULT '',
  last_evidence_at  TIMESTAMP,
  created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_intel_questions_ext ON intel_questions(project_id, venue, external_id) WHERE external_id <> '';
CREATE INDEX ix_intel_questions_status ON intel_questions(project_id, status, deadline);

-- Market-implied probability over time: the baseline every forecast is
-- scored against.
CREATE TABLE intel_question_prices (
  id          INTEGER PRIMARY KEY,
  question_id INTEGER NOT NULL,
  price       REAL NOT NULL,
  observed_at TIMESTAMP NOT NULL,
  source      TEXT NOT NULL
);
CREATE INDEX ix_intel_qprices ON intel_question_prices(question_id, observed_at);

-- Evidence linked to a question, with the relevance that linked it.
CREATE TABLE intel_question_evidence (
  question_id INTEGER NOT NULL,
  evidence_id INTEGER NOT NULL,
  relevance   REAL NOT NULL DEFAULT 0,
  linked_at   TIMESTAMP NOT NULL,
  PRIMARY KEY (question_id, evidence_id)
);

CREATE TABLE intel_forecasts (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  question_id     INTEGER NOT NULL,
  revision        INTEGER NOT NULL,
  probability     REAL,
  abstained       INTEGER NOT NULL DEFAULT 0,
  abstain_reason  TEXT NOT NULL DEFAULT '',
  prior           REAL,
  market_price    REAL,
  adjustment      REAL NOT NULL DEFAULT 0,
  evidence_cutoff TIMESTAMP NOT NULL,
  snapshot_hash   TEXT NOT NULL,
  evidence_ids    TEXT NOT NULL DEFAULT '[]',
  model           TEXT NOT NULL,
  model_version   TEXT NOT NULL,
  assumptions     TEXT NOT NULL DEFAULT '[]',
  explanation     TEXT NOT NULL DEFAULT '',
  trigger         TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX ux_intel_forecast_rev ON intel_forecasts(question_id, revision);
CREATE INDEX ix_intel_forecasts_project ON intel_forecasts(project_id, created_at DESC);

CREATE TABLE intel_scenarios (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  base_as_of  TIMESTAMP NOT NULL,
  shocks      TEXT NOT NULL,
  rules       TEXT NOT NULL,
  result      TEXT NOT NULL,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_intel_scenarios ON intel_scenarios(project_id, created_at DESC);
