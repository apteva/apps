-- Keep task routing records when a peer stops advertising an agent.
ALTER TABLE a2a_remote_agents ADD COLUMN directory_visible INTEGER NOT NULL DEFAULT 1;

-- Panel totals and pagination must remain responsive as the ledger grows.
CREATE INDEX idx_a2a_deliveries_task ON a2a_deliveries(task_id, delivered);
CREATE INDEX idx_a2a_tasks_recent ON a2a_tasks(project_id, updated_at DESC, id DESC);
