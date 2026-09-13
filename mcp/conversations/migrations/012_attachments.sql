CREATE TABLE conversation_attachments (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL,
 metadata TEXT NOT NULL,
 content BLOB NOT NULL,
 sha256 TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX attachments_chat ON conversation_attachments(conversation_id);
