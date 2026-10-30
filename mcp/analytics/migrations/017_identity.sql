-- Analytics v0.18 — server-side identity and acquisition attribution.
--
-- The static-site tag only knows an anonymous visitor_id (props.visitor_id).
-- A backend that has authenticated someone calls analytics_identify to link
-- that visitor to its user ID; the browser never asserts identity.

-- One visitor belongs to at most one user at a time; a user may have many
-- visitors (devices, browsers, cleared storage).
CREATE TABLE IF NOT EXISTS identity_links (
  project_id TEXT NOT NULL,
  visitor_id TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  linked_at  INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, visitor_id)
);
CREATE INDEX IF NOT EXISTS ix_identity_links_user ON identity_links(project_id, user_id);

-- Other identifiers the same user is known by in other apps (a billing
-- customer ID, a CRM contact ID), so lifecycle events that carry those IDs
-- can be joined to the user's acquisition without the emitting app knowing
-- about analytics.
CREATE TABLE IF NOT EXISTS identity_aliases (
  project_id TEXT NOT NULL,
  alias      TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, alias)
);
CREATE INDEX IF NOT EXISTS ix_identity_aliases_user ON identity_aliases(project_id, user_id);

-- Visitor lookups over tag traffic: backfill on identify and attribution.
CREATE INDEX IF NOT EXISTS ix_events_web_visitor
  ON events(project_id, json_extract(props, '$.visitor_id'))
  WHERE source = 'web';
