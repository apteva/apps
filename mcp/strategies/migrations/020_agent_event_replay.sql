CREATE TABLE backtest_agent_decisions (
 run_id INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
 input_id TEXT NOT NULL,
 observation_sha256 TEXT NOT NULL,
 decision_json TEXT NOT NULL,
 PRIMARY KEY(run_id,input_id)
);
-- Only populated inside a disposable replay environment. The parent stages a
-- turn before starting its agent; all agent-visible MCP tools then use this row.
CREATE TABLE backtest_agent_mailbox (
 id INTEGER PRIMARY KEY CHECK(id=1),
 turn_json TEXT NOT NULL
);
