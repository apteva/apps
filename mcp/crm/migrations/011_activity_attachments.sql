CREATE TABLE contact_activity_attachments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  activity_id INTEGER NOT NULL REFERENCES contact_activities(id) ON DELETE CASCADE,
  messaging_attachment_id INTEGER NOT NULL,
  storage_id INTEGER,
  url TEXT,
  filename TEXT,
  content_type TEXT,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  content_id TEXT,
  disposition TEXT,
  source TEXT,
  provider_ref TEXT,
  processing_status TEXT NOT NULL DEFAULT 'ready',
  processing_error TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_activity_attachment_messaging
  ON contact_activity_attachments(project_id, activity_id, messaging_attachment_id);

CREATE INDEX idx_activity_attachments_activity
  ON contact_activity_attachments(project_id, activity_id, id);
