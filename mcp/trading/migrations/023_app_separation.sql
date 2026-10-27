-- Portfolios and execution stay in Trading. Existing research is retained for explicit import.
ALTER TABLE orders ADD COLUMN origin_app TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN origin_install_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN decision_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN external_strategy_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN external_strategy_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE research_imports (source_install_id INTEGER NOT NULL, project_id TEXT NOT NULL, imported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(source_install_id,project_id));
UPDATE portfolio_strategy_assignments SET status='paused' WHERE status='active';
