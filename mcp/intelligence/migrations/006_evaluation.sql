-- Evaluation: frozen historical datasets, reproducible backtest runs and
-- the per-item predictions behind them.
--
-- Backtests live only in these tables. Live (prospective) forecasts stay in
-- intel_forecasts and are scored by the ledger, so the two never mix.

CREATE TABLE IF NOT EXISTS intel_eval_datasets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'building', -- building | ready | failed
    params       TEXT    NOT NULL DEFAULT '{}',
    content_hash TEXT    NOT NULL DEFAULT '',
    n_items      INTEGER NOT NULL DEFAULT 0,
    skipped      TEXT    NOT NULL DEFAULT '{}',        -- reason -> count
    error        TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    fetched_at   TEXT
);

-- One resolved binary question with its trade-by-trade price history as it
-- was fetched. prices is JSON [[unix_ms, probability], ...] ascending.
CREATE TABLE IF NOT EXISTS intel_eval_items (
    dataset_id  INTEGER NOT NULL,
    idx         INTEGER NOT NULL,
    venue       TEXT    NOT NULL,
    external_id TEXT    NOT NULL,
    cluster     TEXT    NOT NULL, -- linked questions share one (bootstrap unit)
    text        TEXT    NOT NULL,
    url         TEXT    NOT NULL DEFAULT '',
    opened_at   TEXT    NOT NULL,
    closes_at   TEXT    NOT NULL,
    resolved_at TEXT    NOT NULL,
    outcome     INTEGER NOT NULL,
    prices      TEXT    NOT NULL,
    PRIMARY KEY (dataset_id, idx)
);

CREATE TABLE IF NOT EXISTS intel_eval_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    dataset_id   INTEGER NOT NULL,
    status       TEXT    NOT NULL, -- running | done | failed
    params       TEXT    NOT NULL,
    code_version TEXT    NOT NULL,
    report       TEXT,
    error        TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    finished_at  TEXT
);

CREATE TABLE IF NOT EXISTS intel_eval_predictions (
    run_id        INTEGER NOT NULL,
    item_idx      INTEGER NOT NULL,
    horizon_hours REAL    NOT NULL,
    method        TEXT    NOT NULL,
    cutoff        TEXT    NOT NULL,
    probability   REAL,
    status        TEXT    NOT NULL, -- ok | abstain | error | excluded
    detail        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, item_idx, horizon_hours, method)
);
