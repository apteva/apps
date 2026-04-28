-- CRM v0.1 — contacts only.
--
-- Every table is partitioned by project_id so the same schema serves
-- both `scope: project` (one install per project; project_id is a
-- safety partition) and `scope: global` (one install across projects;
-- project_id is the isolation boundary, the app filters every read by
-- the calling agent's project).

CREATE TABLE contacts (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  first_name      TEXT,
  last_name       TEXT,
  display_name    TEXT,
  pronouns        TEXT,

  -- Denormalised "primary" channels for index seeks. The truth lives
  -- in contact_channels — these mirror the row tagged is_primary=1.
  primary_email   TEXT,
  primary_phone   TEXT,                -- E.164 normalised when possible

  -- Light professional context. Companies-as-entities is a v0.2 add.
  company         TEXT,
  job_title       TEXT,

  -- Workflow.
  owner_user_id   INTEGER,
  status          TEXT    NOT NULL DEFAULT 'active',  -- active | archived | spam | merged
  source          TEXT,                                -- "agent:<id>" | "human:<id>" | "<integration-name>" | "manual"

  first_contact_at  TIMESTAMP,
  last_contact_at   TIMESTAMP,
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at        TIMESTAMP                          -- soft delete
);
CREATE INDEX ix_contacts_proj    ON contacts(project_id, status);
CREATE INDEX ix_contacts_email   ON contacts(project_id, primary_email);
CREATE INDEX ix_contacts_phone   ON contacts(project_id, primary_phone);
CREATE INDEX ix_contacts_company ON contacts(project_id, company);
CREATE INDEX ix_contacts_updated ON contacts(project_id, updated_at DESC);

-- Multi-value channels — the relation that lets every modern CRM
-- (Pipedrive, Attio, Folk) handle "Alice has work + personal email"
-- cleanly without N email columns. Unique on (project, kind, value)
-- so we can dedup at insert time.
CREATE TABLE contact_channels (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  contact_id  INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL,                       -- email | phone | linkedin | twitter | github | website | other_url
  value       TEXT    NOT NULL,                       -- normalised
  label       TEXT,                                    -- "work" | "personal" | "home" | …
  is_primary  INTEGER NOT NULL DEFAULT 0,
  verified_at TIMESTAMP,
  source      TEXT,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_channel        ON contact_channels(project_id, kind, value);
CREATE INDEX        ix_channel_contact ON contact_channels(contact_id);

-- Typed custom-attribute schema. Keyed per project so two projects
-- can share an install (scope: global) and still define different
-- attributes without collision.
CREATE TABLE contact_attribute_defs (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  key          TEXT    NOT NULL,                      -- "renewal_date" | "msrp_segment" | …
  label        TEXT    NOT NULL,
  type         TEXT    NOT NULL,                      -- text | number | date | bool | select | multi_select | url
  enum_values  TEXT,                                   -- JSON array when type ∈ {select, multi_select}
  required     INTEGER NOT NULL DEFAULT 0,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  is_system    INTEGER NOT NULL DEFAULT 0,             -- shipped by the app vs. user-defined
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_attr_def ON contact_attribute_defs(project_id, key);

-- Typed values. Per-attribute provenance (Folk's pattern) so the
-- dashboard can show "agent set this" vs "human set this" vs "Apollo
-- enrichment set this".
CREATE TABLE contact_attributes (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  contact_id    INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  def_id        INTEGER NOT NULL REFERENCES contact_attribute_defs(id),
  value_text    TEXT,                                  -- exactly one of these is set, by def.type
  value_number  REAL,
  value_date    DATE,
  value_bool    INTEGER,
  source        TEXT,
  source_detail TEXT,
  set_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_attr_value  ON contact_attributes(contact_id, def_id);
CREATE INDEX        ix_attr_lookup ON contact_attributes(project_id, def_id, value_text);

-- Tags as join table — never JSON. Killing filterability is the
-- second-most-common CRM v0.1 mistake (after string-email).
CREATE TABLE contact_tags (
  project_id TEXT    NOT NULL,
  contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  tag_name   TEXT    NOT NULL,
  PRIMARY KEY (project_id, contact_id, tag_name)
);

-- Append-only timeline. Even contacts-only v0.1 needs "when did I
-- last talk to this person?" — that's this table.
CREATE TABLE contact_activities (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  contact_id   INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL,                      -- email_sent | email_received | call | meeting | note | system
  body         TEXT    NOT NULL,
  occurred_at  TIMESTAMP NOT NULL,
  source       TEXT,
  source_detail TEXT,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_act_contact ON contact_activities(contact_id, occurred_at DESC);
CREATE INDEX ix_act_kind    ON contact_activities(project_id, kind, occurred_at DESC);

-- Append-only merge log. Every merge is recorded — never silent.
CREATE TABLE contact_merges (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  loser_id    INTEGER NOT NULL,
  winner_id   INTEGER NOT NULL REFERENCES contacts(id),
  source      TEXT,
  notes       TEXT,
  merged_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_merge_winner ON contact_merges(project_id, winner_id);
