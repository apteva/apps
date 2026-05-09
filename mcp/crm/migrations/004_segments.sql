-- Segments — saved filters over contacts.
--
-- Two flavours:
--   - dynamic: definition_json is the source of truth; membership is
--     evaluated at query time (every segments_eval call recomputes).
--   - static:  same definition + a frozen snapshot in
--     contact_segment_snapshots; segments_eval reads the snapshot.
--     Used as the audience for a campaign send so the recipient list
--     doesn't shift mid-blast.
--
-- Optionally scoped to a list (list_id non-null). When scoped, the
-- segment definition is implicitly AND-ed with `in_list(list_id)` so
-- the user doesn't have to repeat themselves.

CREATE TABLE contact_segments (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  list_id         INTEGER REFERENCES contact_lists(id) ON DELETE SET NULL,

  name            TEXT    NOT NULL,
  description     TEXT,

  kind            TEXT    NOT NULL DEFAULT 'dynamic',  -- dynamic | static
  definition_json TEXT    NOT NULL,                    -- JSON predicate list

  -- TTL-cached count so the panel doesn't re-eval on every render.
  -- segments_count refreshes when older than ~5 minutes.
  cached_count    INTEGER,
  cached_at       TIMESTAMP,

  archived_at     TIMESTAMP,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_segment_name ON contact_segments(project_id, name);
CREATE INDEX ix_segment_active ON contact_segments(project_id, archived_at);
CREATE INDEX ix_segment_list ON contact_segments(project_id, list_id) WHERE list_id IS NOT NULL;

-- Snapshot — populated only for kind='static'. One row per
-- (segment, contact) at the moment of materialisation. Cleared and
-- repopulated on each segments_materialise call.
CREATE TABLE contact_segment_snapshots (
  segment_id      INTEGER NOT NULL REFERENCES contact_segments(id) ON DELETE CASCADE,
  contact_id      INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  project_id      TEXT    NOT NULL,
  snapshotted_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (segment_id, contact_id)
);
CREATE INDEX ix_segment_snap_contact ON contact_segment_snapshots(project_id, contact_id);
