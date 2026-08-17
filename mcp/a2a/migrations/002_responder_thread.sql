-- v0.2.1: remember which thread on the responder side owns a task.
-- Set on the responder's first agent_reply; later follow-ups from the
-- requester route to that thread instead of the responder's main, so a
-- delegated worker keeps its own conversation.

ALTER TABLE a2a_tasks ADD COLUMN to_thread_id TEXT NOT NULL DEFAULT '';
