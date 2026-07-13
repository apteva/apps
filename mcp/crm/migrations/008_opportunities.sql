-- CRM opportunities and sales pipelines.
--
-- Contacts remain identity records. Opportunities are the pipeline-tracked
-- sales motions linked to contacts: stage belongs to the opportunity, not
-- the contact.

CREATE TABLE crm_pipelines (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT,
  is_default  INTEGER NOT NULL DEFAULT 0,
  archived_at TIMESTAMP,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_crm_pipeline_name
  ON crm_pipelines(project_id, name);
CREATE UNIQUE INDEX ux_crm_pipeline_default
  ON crm_pipelines(project_id)
  WHERE is_default = 1 AND archived_at IS NULL;
CREATE INDEX ix_crm_pipelines_project
  ON crm_pipelines(project_id, archived_at);

CREATE TABLE crm_pipeline_stages (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  pipeline_id INTEGER NOT NULL REFERENCES crm_pipelines(id) ON DELETE CASCADE,
  name        TEXT    NOT NULL,
  position    INTEGER NOT NULL DEFAULT 0,
  category    TEXT    NOT NULL DEFAULT 'open', -- open | won | lost
  probability REAL,
  archived_at TIMESTAMP,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_crm_stage_position
  ON crm_pipeline_stages(project_id, pipeline_id, position)
  WHERE archived_at IS NULL;
CREATE INDEX ix_crm_stages_pipeline
  ON crm_pipeline_stages(project_id, pipeline_id, archived_at, position);

CREATE TABLE crm_opportunities (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  contact_id          INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  pipeline_id         INTEGER NOT NULL REFERENCES crm_pipelines(id),
  stage_id            INTEGER NOT NULL REFERENCES crm_pipeline_stages(id),
  title               TEXT    NOT NULL,
  status              TEXT    NOT NULL DEFAULT 'open', -- open | won | lost
  value               REAL,
  currency            TEXT,
  offer_key           TEXT,
  offer_name          TEXT,
  source              TEXT,
  source_site         TEXT,
  sender_identity     TEXT,
  owner               TEXT,
  expected_close_date DATE,
  closed_at           TIMESTAMP,
  lost_reason         TEXT,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at         TIMESTAMP
);
CREATE INDEX ix_crm_opp_project_stage
  ON crm_opportunities(project_id, pipeline_id, stage_id, status, updated_at DESC);
CREATE INDEX ix_crm_opp_contact
  ON crm_opportunities(project_id, contact_id, status, updated_at DESC);
CREATE INDEX ix_crm_opp_offer
  ON crm_opportunities(project_id, offer_key, status, updated_at DESC);

CREATE TABLE crm_opportunity_stage_history (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT    NOT NULL,
  opportunity_id INTEGER NOT NULL REFERENCES crm_opportunities(id) ON DELETE CASCADE,
  from_stage_id  INTEGER,
  to_stage_id    INTEGER NOT NULL,
  from_status    TEXT,
  to_status      TEXT,
  note           TEXT,
  source         TEXT,
  changed_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_crm_opp_stage_history
  ON crm_opportunity_stage_history(project_id, opportunity_id, changed_at DESC);
