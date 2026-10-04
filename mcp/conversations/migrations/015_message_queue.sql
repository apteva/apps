-- Queue-first user turns. The queue state lives on the durable message so a
-- remount can render the same controls without relying on in-memory activity.
ALTER TABLE messages ADD COLUMN queue_behavior TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE messages ADD COLUMN queue_state TEXT NOT NULL DEFAULT 'released';
ALTER TABLE messages ADD COLUMN queue_position INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_messages_queue
    ON messages(conversation_id, queue_state, queue_position)
    WHERE role = 'user' AND queue_state = 'queued';
