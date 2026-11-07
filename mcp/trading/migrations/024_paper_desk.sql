-- Opt-in, agent-authenticated paper-desk approvals and durable exit plans.
CREATE TABLE paper_desks (
 portfolio_id INTEGER PRIMARY KEY REFERENCES portfolios(id), project_id TEXT NOT NULL,
 manager_id INTEGER NOT NULL, reviewer_id INTEGER NOT NULL, executor_id INTEGER NOT NULL,
 max_planned_loss REAL NOT NULL DEFAULT 25, max_order_notional REAL NOT NULL DEFAULT 1000,
 min_cash_pct REAL NOT NULL DEFAULT 60, enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE paper_decisions (
 portfolio_id INTEGER NOT NULL REFERENCES portfolios(id), decision_id TEXT NOT NULL,
 packet_json TEXT NOT NULL, packet_hash TEXT NOT NULL, status TEXT NOT NULL,
 manager_id INTEGER NOT NULL, reviewer_id INTEGER NOT NULL DEFAULT 0,
 review_ref TEXT NOT NULL DEFAULT '', review_reason TEXT NOT NULL DEFAULT '',
 approved_at TEXT NOT NULL DEFAULT '', approval_expires_at TEXT NOT NULL DEFAULT '',
 order_id TEXT NOT NULL DEFAULT '', exit_order_id TEXT NOT NULL DEFAULT '',
 exit_at TEXT NOT NULL DEFAULT '', exit_reason TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(portfolio_id,decision_id)
);
CREATE UNIQUE INDEX paper_decisions_entry ON paper_decisions(order_id) WHERE order_id<>'';
CREATE UNIQUE INDEX paper_decisions_exit ON paper_decisions(exit_order_id) WHERE exit_order_id<>'';
CREATE TABLE paper_observations (
 portfolio_id INTEGER NOT NULL REFERENCES portfolios(id), observation_id TEXT NOT NULL,
 setup TEXT NOT NULL, symbol TEXT NOT NULL, verdict TEXT NOT NULL, reason TEXT NOT NULL,
 reference_price REAL NOT NULL, observed_at TEXT NOT NULL,
 PRIMARY KEY(portfolio_id,observation_id)
);
