CREATE TABLE IF NOT EXISTS commerce_analytics_deliveries (
 project_id TEXT NOT NULL, sale_id INTEGER NOT NULL, payload_json TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 delivered_at TEXT, last_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(project_id,sale_id)
);
