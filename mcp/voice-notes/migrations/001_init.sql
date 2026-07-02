CREATE TABLE voice_notes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft',
  storage_file_id TEXT NOT NULL DEFAULT '',
  storage_url TEXT NOT NULL DEFAULT '',
  file_name TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  transcript_status TEXT NOT NULL DEFAULT 'none',
  transcript_text TEXT NOT NULL DEFAULT '',
  transcript_language TEXT NOT NULL DEFAULT '',
  transcript_provider TEXT NOT NULL DEFAULT '',
  transcript_model TEXT NOT NULL DEFAULT '',
  transcript_segments_json TEXT NOT NULL DEFAULT '[]',
  error_message TEXT NOT NULL DEFAULT '',
  tags_json TEXT NOT NULL DEFAULT '[]',
  recorded_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_voice_notes_project_created
  ON voice_notes(project_id, created_at DESC);

CREATE INDEX ix_voice_notes_project_status
  ON voice_notes(project_id, status, created_at DESC);
