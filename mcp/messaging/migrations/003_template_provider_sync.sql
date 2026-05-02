-- v0.4.0: provider-mirrored templates.
--
-- v0.3 templates were always local: body_text + {{var}} substitution
-- rendered server-side. v0.4 adds the option to mirror provider-side
-- pre-approved templates (Twilio Content / Meta WhatsApp) into the
-- same table, so send_message_template can dispatch via the provider's
-- ContentSid for messages that have to use approved content.
--
-- Per-row schema additions (all NULL on legacy rows = local templates):

ALTER TABLE templates ADD COLUMN provider_template_id TEXT;
-- Twilio: ContentSid (HXxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx).
-- NULL means a local template — body_text/body_html are the source
-- of truth and we render {{var}} ourselves before sending.

ALTER TABLE templates ADD COLUMN provider_status TEXT;
-- 'approved' | 'pending' | 'rejected' | 'deleted' | NULL.
-- For provider-mirrored rows, refreshed by the sync tools. Sends
-- against non-approved rows fail-fast with a precise error.

ALTER TABLE templates ADD COLUMN var_style TEXT NOT NULL DEFAULT 'named';
-- 'named'    — local templates with {{var_name}} placeholders, the
--              v0.3 default. Substitution happens in messaging.
-- 'numbered' — Twilio Content templates with {{1}} {{2}} placeholders.
--              vars is JSON-stringified and shipped as ContentVariables.

ALTER TABLE templates ADD COLUMN last_synced_at TIMESTAMP;
-- Last time this row was refreshed from the provider. NULL for
-- local-only rows. Read by the auto-sync TTL in template_list to
-- decide whether to kick off a background refresh.

-- Old uniqueness was (project_id, channel, name). With mirrored
-- rows we want two distinct uniqueness rules:
--   1. local rows are unique by (project, channel, name) — same as v0.3.
--   2. provider rows are unique by (project, provider_template_id) —
--      the ContentSid is the immutable handle, even if the operator
--      renames the template upstream.
-- Two partial unique indexes do this cleanly in SQLite.

DROP INDEX IF EXISTS ix_tpl_name;

CREATE UNIQUE INDEX ix_tpl_name_local
  ON templates(project_id, channel, name)
  WHERE deleted_at IS NULL AND provider_template_id IS NULL;

CREATE UNIQUE INDEX ix_tpl_provider
  ON templates(project_id, provider_template_id)
  WHERE deleted_at IS NULL AND provider_template_id IS NOT NULL;

-- Per-(project, channel) sync bookkeeping. The auto-sync TTL on
-- template_list uses last_synced_at here (rather than per-template)
-- because the sync is a list-level call — refreshing the freshest
-- row's last_synced_at would wrongly mark stale rows as fresh too.
--
-- in_progress is a soft hint set by Go-side mutex; the column is
-- here so a future sidecar restart can recover from a crashed sync
-- by clearing stale flags.

CREATE TABLE template_sync_state (
  project_id     TEXT NOT NULL,
  channel        TEXT NOT NULL,
  last_synced_at TIMESTAMP,
  in_progress    INTEGER NOT NULL DEFAULT 0,
  last_error     TEXT,
  last_synced_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, channel)
);
