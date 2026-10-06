-- Durable message ordering survives same-second sends and VACUUM.
CREATE TABLE dm_messages_ordered (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    community_id TEXT NOT NULL,
    dm_thread_id TEXT NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
    author_id TEXT NOT NULL REFERENCES members(id),
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO dm_messages_ordered (id, community_id, dm_thread_id, author_id, body, created_at)
SELECT id, community_id, dm_thread_id, author_id, body, created_at FROM dm_messages ORDER BY rowid;
DROP TABLE dm_messages;
ALTER TABLE dm_messages_ordered RENAME TO dm_messages;
CREATE INDEX idx_dm_messages_thread ON dm_messages(dm_thread_id, seq);
ALTER TABLE dm_participants ADD COLUMN last_read_seq INTEGER NOT NULL DEFAULT 0;
UPDATE dm_participants SET last_read_seq = COALESCE((
 SELECT MAX(seq) FROM dm_messages
 WHERE dm_thread_id = dm_participants.dm_thread_id AND created_at <= dm_participants.last_read_at
), 0);

CREATE TABLE quiz_attempts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 quiz_id TEXT NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
 answers_json TEXT NOT NULL,
 score INTEGER NOT NULL,
 passed INTEGER NOT NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_quiz_attempts_member ON quiz_attempts(quiz_id, member_id, id DESC);
CREATE TABLE assignment_submissions (
 assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
 body TEXT NOT NULL,
 updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(assignment_id, member_id)
);
