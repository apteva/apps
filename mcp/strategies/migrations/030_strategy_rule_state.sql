-- Live rule-program checkpoint per assignment: variables, latches and the
-- warmup marker. Candles are rebuilt from recent bars on every evaluation.
CREATE TABLE IF NOT EXISTS strategy_rule_state (
    assignment_id INTEGER PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
