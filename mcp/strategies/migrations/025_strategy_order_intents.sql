-- Persist the exact submission before crossing the app boundary.
CREATE TABLE strategy_order_intents (request_key TEXT PRIMARY KEY, project_id TEXT NOT NULL, portfolio_id INTEGER NOT NULL, request_json TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
