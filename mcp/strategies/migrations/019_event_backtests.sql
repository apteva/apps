CREATE TABLE backtest_simulations (
  run_id INTEGER PRIMARY KEY REFERENCES backtest_runs(id) ON DELETE CASCADE,
  spec_json TEXT NOT NULL,
  inputs_json TEXT NOT NULL,
  input_sha256 TEXT NOT NULL,
  state_json TEXT,
  revision INTEGER NOT NULL DEFAULT 0,
  result_sha256 TEXT
);
CREATE TABLE backtest_simulation_outputs (
  run_id INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL,
  output_json TEXT NOT NULL,
  PRIMARY KEY (run_id, sequence)
);
-- A process restart leaves durable checkpoints available for explicit resume.
