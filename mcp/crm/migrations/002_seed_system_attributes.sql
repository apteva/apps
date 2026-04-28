-- System attribute defs that ship with every install, every project.
-- These are commonly-needed fields that didn't justify their own SQL
-- column on contacts (low cardinality, optional, app-defined).
--
-- The (project_id) is filled in lazily — when a project's first
-- contact is touched, the app inserts these defs scoped to that
-- project_id. We can't do it here because project_id values aren't
-- known at install time (especially for `scope: global`).
--
-- This migration creates a *template* table the app code reads from
-- when seeding a project's attribute_defs.

CREATE TABLE _system_attribute_templates (
  key          TEXT PRIMARY KEY,
  label        TEXT NOT NULL,
  type         TEXT NOT NULL,
  enum_values  TEXT,
  sort_order   INTEGER NOT NULL DEFAULT 0
);

INSERT INTO _system_attribute_templates (key, label, type, enum_values, sort_order) VALUES
  ('timezone',       'Timezone',        'text',   NULL,                                                         10),
  ('industry',       'Industry',        'text',   NULL,                                                         20),
  ('seniority',      'Seniority',       'select', '["ic","manager","director","vp","exec","founder","other"]', 30),
  ('lead_score',     'Lead score',      'number', NULL,                                                         40),
  ('do_not_contact', 'Do not contact',  'bool',   NULL,                                                         50),
  ('lifecycle',      'Lifecycle stage', 'select', '["lead","customer","churned","prospect","other"]',           60),
  ('preferred_channel', 'Preferred channel', 'select', '["email","phone","sms","chat","whatsapp"]',             70),
  ('linkedin_url',   'LinkedIn URL',    'url',    NULL,                                                         80),
  ('website',        'Website',         'url',    NULL,                                                         90),
  ('birthday',       'Birthday',        'date',   NULL,                                                        100);
