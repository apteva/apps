-- conversations v0.14: durable inbound delivery and per-agent thread state.

ALTER TABLE conversations ADD COLUMN directive TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN phase TEXT NOT NULL DEFAULT 'final';

CREATE TABLE IF NOT EXISTS conversation_agent_threads (
    conversation_id TEXT    NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    agent_id        INTEGER NOT NULL,
    thread_id       TEXT    NOT NULL,
    desired_hash    TEXT    NOT NULL DEFAULT '',
    applied_hash    TEXT    NOT NULL DEFAULT '',
    last_error      TEXT    NOT NULL DEFAULT '',
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conversation_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_conversation_agent_threads_thread
    ON conversation_agent_threads(agent_id, thread_id);
