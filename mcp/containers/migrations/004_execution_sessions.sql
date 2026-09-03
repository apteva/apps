-- Optional persistent shell identity for stateful workspace command sessions.

ALTER TABLE containers_executions ADD COLUMN session_key TEXT NOT NULL DEFAULT '';
