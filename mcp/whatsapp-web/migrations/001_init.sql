CREATE TABLE IF NOT EXISTS accounts (
  project_id TEXT NOT NULL,
  jid TEXT NOT NULL,
  phone TEXT NOT NULL,
  push_name TEXT NOT NULL DEFAULT '',
  business_name TEXT NOT NULL DEFAULT '',
  platform TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'disconnected',
  last_error TEXT NOT NULL DEFAULT '',
  connected_at TEXT,
  disconnected_at TEXT,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, jid)
);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  direction TEXT NOT NULL CHECK (direction IN ('in', 'out')),
  from_addr TEXT NOT NULL DEFAULT '',
  to_addr TEXT NOT NULL DEFAULT '',
  chat_jid TEXT NOT NULL DEFAULT '',
  sender_jid TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  body_text TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_project_time ON messages(project_id, occurred_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_project_provider ON messages(project_id, message_id) WHERE message_id <> '';
