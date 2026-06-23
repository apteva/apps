-- media_identities tracks reusable provider-side identities such as
-- voices and avatars. These are not media generation outputs; they are
-- stable ids later used by TTS/avatar generation, personas, and composer
-- flows.

CREATE TABLE media_identities (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id           TEXT    NOT NULL,
  kind                 TEXT    NOT NULL, -- voice | avatar
  provider             TEXT    NOT NULL,
  name                 TEXT    NOT NULL,
  source_type          TEXT    NOT NULL DEFAULT '', -- prompt | audio | photo | video
  provider_identity_id TEXT    DEFAULT '',
  provider_job_id      TEXT    DEFAULT '',
  provider_group_id    TEXT    DEFAULT '',
  source_ref           TEXT    DEFAULT '',
  prompt               TEXT    DEFAULT '',
  preview_url          TEXT    DEFAULT '',
  status               TEXT    NOT NULL DEFAULT 'ready', -- draft | queued | training | ready | failed
  error                TEXT    DEFAULT '',
  metadata_json        TEXT    NOT NULL DEFAULT '{}',
  created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_media_identities_project_kind
  ON media_identities(project_id, kind, id DESC);

CREATE INDEX idx_media_identities_provider_identity
  ON media_identities(project_id, kind, provider, provider_identity_id)
  WHERE provider_identity_id <> '';
