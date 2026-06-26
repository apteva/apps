-- v0.13.35: normalized attachments for outbound and inbound messages.

CREATE TABLE IF NOT EXISTS message_attachments (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  message_id      INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  storage_id      INTEGER,
  url             TEXT,
  filename        TEXT NOT NULL,
  content_type    TEXT,
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  content_id      TEXT,
  disposition     TEXT NOT NULL DEFAULT 'attachment',
  source          TEXT NOT NULL DEFAULT 'storage',
  provider_ref    TEXT,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_msg_attach_msg
  ON message_attachments(message_id, id);

CREATE INDEX IF NOT EXISTS ix_msg_attach_project
  ON message_attachments(project_id, created_at DESC);
