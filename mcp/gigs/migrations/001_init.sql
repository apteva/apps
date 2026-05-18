-- Gigs v0.1 — agent→human work assignment with a composable
-- instruction library and template engine.
--
-- Every table is partitioned by project_id so the same schema serves
-- both `scope: project` (one install per project; project_id is a
-- safety partition) and `scope: global` (one install across projects;
-- project_id is the isolation boundary).

-- ─── Workers (promoted CRM contacts) ────────────────────────────────
-- A worker row is a *promotion* of a CRM contact. Name, email, phone,
-- channels — all of that lives in CRM and is resolved at runtime via
-- contacts_get. This table stores only the gig-specific overlay.
CREATE TABLE workers (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  contact_id      INTEGER NOT NULL,                  -- → crm.contacts.id
  status          TEXT    NOT NULL DEFAULT 'active', -- active | paused | retired
  default_channel TEXT,                              -- email | sms | whatsapp | null (let CRM pick)
  notes           TEXT,
  rating_avg      REAL    NOT NULL DEFAULT 0,
  accepted_count  INTEGER NOT NULL DEFAULT 0,
  rejected_count  INTEGER NOT NULL DEFAULT 0,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at     TIMESTAMP
);
CREATE UNIQUE INDEX ux_worker_contact ON workers(project_id, contact_id);
CREATE INDEX        ix_workers_status ON workers(project_id, status);

-- Skills are project-local. Workers carry a level per skill (1..5).
CREATE TABLE skills (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  slug        TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_skill_slug ON skills(project_id, slug);

CREATE TABLE worker_skills (
  worker_id   INTEGER NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
  skill_id    INTEGER NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  level       INTEGER NOT NULL DEFAULT 3 CHECK(level BETWEEN 1 AND 5),
  PRIMARY KEY (worker_id, skill_id)
);
CREATE INDEX ix_worker_skills_skill ON worker_skills(skill_id);

-- ─── Instruction library ────────────────────────────────────────────
-- An instruction is the atomic reusable unit. Heads are stable for
-- agent/template references; versions are immutable bodies that
-- templates pin to.
CREATE TABLE instructions (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  slug                TEXT    NOT NULL,
  name                TEXT    NOT NULL,
  kind                TEXT    NOT NULL,
  -- text | audio | video | image | document | link | script
  -- | warning | example
  -- | checklist_item | confirmation | timer_hint
  -- | input_short_text | input_long_text | input_number | input_date
  -- | input_choice | input_multi_choice | input_rating | input_yes_no
  -- | input_photo | input_audio_recording | input_video_recording
  -- | input_file | input_signature | input_location
  current_version_id  INTEGER,
  archived_at         TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_instruction_slug ON instructions(project_id, slug);
CREATE INDEX        ix_instructions_kind ON instructions(project_id, kind, archived_at);

CREATE TABLE instruction_versions (
  id                       INTEGER PRIMARY KEY,
  instruction_id           INTEGER NOT NULL REFERENCES instructions(id) ON DELETE CASCADE,
  version                  INTEGER NOT NULL,
  status                   TEXT    NOT NULL DEFAULT 'draft', -- draft | active | archived
  -- body_json shape depends on instruction.kind:
  --   text:                  {markdown}
  --   audio:                 {storage_file_id, transcript?}
  --   video:                 {storage_file_id, poster_file_id?, caption?}
  --   image:                 {storage_file_id, caption?}
  --   document:              {storage_file_id, display?}
  --   link:                  {url, label}
  --   script:                {lines: [string]}
  --   warning:               {text, severity}
  --   example:               {good_text?, bad_text?, good_file_id?, bad_file_id?}
  --   checklist_item:        {text, required?}
  --   confirmation:          {text}
  --   timer_hint:            {seconds_suggested}
  --   input_short_text:      {label, placeholder?, required?, max?}
  --   input_long_text:       {label, placeholder?, required?, max?}
  --   input_number:          {label, required?, min?, max?, step?}
  --   input_date:            {label, required?, min?, max?}
  --   input_choice:          {label, required?, options: [{value,label}]}
  --   input_multi_choice:    {label, required?, options: [...], min?, max?}
  --   input_rating:          {label, required?, scale: 5}
  --   input_yes_no:          {label, required?}
  --   input_photo,
  --   input_audio_recording,
  --   input_video_recording,
  --   input_file:            {label, required?, accept_mime?}
  --   input_signature:       {label, required?}
  --   input_location:        {label, required?}
  body_json                TEXT    NOT NULL,
  declared_variables_json  TEXT,                 -- JSON: ["vendor_name", ...]
  default_result_key       TEXT,                 -- for input/do kinds
  result_field_json        TEXT,                 -- JSON Schema fragment {type, properties?, items?, ...}
  created_by               TEXT,
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_instruction_version ON instruction_versions(instruction_id, version);
CREATE INDEX        ix_instruction_version_status ON instruction_versions(instruction_id, status);

-- ─── Templates (compositions of pinned instruction versions) ────────
CREATE TABLE templates (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  slug                TEXT    NOT NULL,
  name                TEXT    NOT NULL,
  kind                TEXT    NOT NULL DEFAULT 'action',
  -- decision | action | creative | expert | micro | physical
  current_version_id  INTEGER,
  archived_at         TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_template_slug ON templates(project_id, slug);

CREATE TABLE template_versions (
  id                          INTEGER PRIMARY KEY,
  template_id                 INTEGER NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
  version                     INTEGER NOT NULL,
  status                      TEXT    NOT NULL DEFAULT 'draft', -- draft | active | archived
  title_template              TEXT    NOT NULL,
  default_deadline_hours      INTEGER,
  default_skill_ids_json      TEXT,                              -- JSON: [int]
  default_priority            TEXT,                              -- low | normal | high
  variable_overrides_json     TEXT,                              -- JSON: {name: {default?, label?}}
  created_by                  TEXT,
  created_at                  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_template_version ON template_versions(template_id, version);

CREATE TABLE template_instructions (
  id                         INTEGER PRIMARY KEY,
  template_version_id        INTEGER NOT NULL REFERENCES template_versions(id) ON DELETE CASCADE,
  instruction_id             INTEGER NOT NULL REFERENCES instructions(id),
  instruction_version_id     INTEGER NOT NULL REFERENCES instruction_versions(id),
  sort_order                 INTEGER NOT NULL,
  result_key                 TEXT,                                -- overrides default_result_key
  overrides_json             TEXT                                 -- per-use body overrides (label tweak, etc.)
);
CREATE INDEX ix_template_instructions ON template_instructions(template_version_id, sort_order);
CREATE INDEX ix_template_instructions_lookup ON template_instructions(instruction_id);

-- ─── Gigs (immutable snapshots) ─────────────────────────────────────
CREATE TABLE gigs (
  id                              INTEGER PRIMARY KEY,
  project_id                      TEXT    NOT NULL,
  template_version_id             INTEGER,                        -- nullable for ad-hoc gigs
  created_by                      TEXT    NOT NULL,               -- "agent:<id>" | "human:<user_id>"
  title                           TEXT    NOT NULL,               -- rendered with vars
  vars_json                       TEXT,                           -- the values supplied at dispatch
  derived_result_schema_json      TEXT    NOT NULL,               -- frozen JSON Schema
  derived_media_manifest_json     TEXT,                           -- frozen [{storage_file_id, role, label}]
  derived_checklist_json          TEXT,                           -- frozen [{result_key, text, required}]
  derived_variables_json          TEXT,                           -- frozen [{name, type, label, required}]
  budget_cents                    INTEGER,                        -- informational only in v0.1
  deadline_at                     TIMESTAMP,
  priority                        TEXT,
  status                          TEXT    NOT NULL,
  -- open | offered | accepted | submitted | reviewed | rejected | cancelled | expired
  result_json                     TEXT,                           -- validated submission, copied from gig_submissions on accept
  rejection_reason                TEXT,
  created_at                      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at                      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at                    TIMESTAMP
);
CREATE INDEX ix_gigs_status ON gigs(project_id, status, deadline_at);
CREATE INDEX ix_gigs_template ON gigs(template_version_id);
CREATE INDEX ix_gigs_created ON gigs(project_id, created_at DESC);

-- The dispatched composition, frozen. Each row is one instruction
-- after variable interpolation. Storage refs are preserved by id;
-- the worker page mints signed URLs at view time.
CREATE TABLE gig_instructions (
  id                              INTEGER PRIMARY KEY,
  gig_id                          INTEGER NOT NULL REFERENCES gigs(id) ON DELETE CASCADE,
  sort_order                      INTEGER NOT NULL,
  instruction_kind                TEXT    NOT NULL,               -- copied from instruction
  rendered_body_json              TEXT    NOT NULL,               -- vars interpolated; storage refs intact
  result_key                      TEXT,                           -- for input/do kinds
  source_instruction_id           INTEGER,                        -- audit only
  source_instruction_version_id   INTEGER                         -- audit only
);
CREATE INDEX ix_gig_instructions ON gig_instructions(gig_id, sort_order);

CREATE TABLE gig_assignments (
  id                          INTEGER PRIMARY KEY,
  gig_id                      INTEGER NOT NULL REFERENCES gigs(id) ON DELETE CASCADE,
  worker_id                   INTEGER NOT NULL REFERENCES workers(id),
  status                      TEXT    NOT NULL,
  -- offered | accepted | declined | submitted | withdrawn
  magic_token                 TEXT    NOT NULL UNIQUE,            -- random 32-byte hex
  offered_at                  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  responded_at                TIMESTAMP,
  submitted_at                TIMESTAMP,
  crm_conversation_id         INTEGER                             -- threading anchor for /inbound correlation
);
CREATE INDEX ix_assignment_worker ON gig_assignments(worker_id, status);
CREATE INDEX ix_assignment_gig    ON gig_assignments(gig_id);
CREATE INDEX ix_assignment_token  ON gig_assignments(magic_token);

CREATE TABLE gig_submissions (
  id                          INTEGER PRIMARY KEY,
  assignment_id               INTEGER NOT NULL REFERENCES gig_assignments(id) ON DELETE CASCADE,
  payload_json                TEXT    NOT NULL,                   -- conforms to gig.derived_result_schema_json
  attachment_file_ids_json    TEXT,                               -- JSON: [int] → storage.files.id
  channel                     TEXT,                               -- web | sms_reply | whatsapp_reply | email_reply
  submitted_at                TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_submission_assignment ON gig_submissions(assignment_id);

-- Append-only audit log. Every status transition writes one row.
CREATE TABLE gig_events (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  gig_id      INTEGER NOT NULL REFERENCES gigs(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL,
  -- created | offered | reminded | accepted_by_worker
  -- | submitted | reviewed | rejected | cancelled | expired | reassigned
  actor       TEXT,
  body        TEXT,
  at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_event_gig ON gig_events(gig_id, at DESC);
CREATE INDEX ix_event_kind ON gig_events(project_id, kind, at DESC);
