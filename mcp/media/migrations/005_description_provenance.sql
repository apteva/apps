-- Media v0.5 — description provenance + cooldown for the auto-describer.
--
-- description_source records who wrote it, so the auto-describer
-- never clobbers a human's prose. Possible values:
--   ''             — never set
--   'human'        — set via panel or media_set_description (default
--                    when no source given to setDescription)
--   'agent'        — set by an agent calling the MCP tool (we don't
--                    distinguish from human in writes today; reserved
--                    for future "agent vs human" telemetry)
--   'ai-generated' — written by the auto-describer
--   'imported'     — bulk-loaded / migrated from elsewhere
--
-- description_attempted_at + description_error are the auto-describer's
-- cooldown bookkeeping: a failed attempt is retried but not within
-- describe_retry_cooldown_seconds, so a misconfigured integration
-- doesn't burn through quota in a tight loop.

ALTER TABLE media ADD COLUMN description_source       TEXT NOT NULL DEFAULT '';
ALTER TABLE media ADD COLUMN description_updated_at   TIMESTAMP;
ALTER TABLE media ADD COLUMN description_attempted_at TIMESTAMP;
ALTER TABLE media ADD COLUMN description_error        TEXT NOT NULL DEFAULT '';

-- Hot path for the auto-describer's candidate query: rows where the
-- describer should consider an attempt. Excludes human-set rows
-- entirely so we never sweep past them; cooldown is enforced with a
-- timestamp check in the worker, not the index.
CREATE INDEX ix_media_auto_describable
  ON media(project_id, created_at DESC)
  WHERE description = ''
    AND description_source != 'human'
    AND description_source != 'agent';
