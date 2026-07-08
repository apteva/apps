CREATE TABLE IF NOT EXISTS testimonials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL DEFAULT '',

  status TEXT NOT NULL DEFAULT 'draft',
  kind TEXT NOT NULL DEFAULT 'text',
  source TEXT NOT NULL DEFAULT 'manual',

  title TEXT NOT NULL DEFAULT '',
  quote TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  rating INTEGER,

  author_name TEXT NOT NULL DEFAULT '',
  author_role TEXT NOT NULL DEFAULT '',
  author_company TEXT NOT NULL DEFAULT '',
  author_email TEXT NOT NULL DEFAULT '',

  media_file_id TEXT NOT NULL DEFAULT '',
  media_url TEXT NOT NULL DEFAULT '',

  consent_status TEXT NOT NULL DEFAULT 'unknown',
  permission_scope TEXT NOT NULL DEFAULT 'internal',

  tags_json TEXT NOT NULL DEFAULT '[]',
  metadata_json TEXT NOT NULL DEFAULT '{}',

  submitted_at TEXT,
  approved_at TEXT,
  published_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_testimonials_project_status_updated
  ON testimonials(project_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_testimonials_project_kind_updated
  ON testimonials(project_id, kind, updated_at DESC);
