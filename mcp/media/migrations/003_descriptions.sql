-- Media v0.3 — user/agent-supplied prose for media files. Lives on
-- the same row as the probe data; the indexer's upsertMedia() touches
-- only the probe columns, so reprobe never wipes a description.
--
-- Cap is conventional, not enforced: agents can write whatever, and
-- v0.3 doesn't truncate. If we need limits later we'll add a CHECK.

ALTER TABLE media ADD COLUMN title       TEXT NOT NULL DEFAULT '';
ALTER TABLE media ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE media ADD COLUMN alt_text    TEXT NOT NULL DEFAULT '';

-- Partial index covering rows that actually carry prose, so a panel
-- "show me anything I've described" query stays cheap regardless of
-- catalog size. ORDER BY updated_at DESC is the usual access pattern.
CREATE INDEX ix_media_has_desc
  ON media(project_id, updated_at DESC)
  WHERE description != '' OR title != '' OR alt_text != '';
