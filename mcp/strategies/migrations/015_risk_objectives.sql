-- Trading v0.7.0 - enforceable portfolio risk and native percentage objectives.

CREATE TABLE portfolio_risk_policies (
  portfolio_id            INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
  project_id              TEXT    NOT NULL,
  risk_level              TEXT    NOT NULL DEFAULT 'custom'
    CHECK (risk_level IN ('conservative','balanced','aggressive','custom')),
  max_daily_loss_pct      REAL    NOT NULL DEFAULT 5 CHECK (max_daily_loss_pct > 0),
  max_drawdown_pct        REAL    NOT NULL DEFAULT 100 CHECK (max_drawdown_pct > 0),
  max_position_pct        REAL    NOT NULL DEFAULT 100 CHECK (max_position_pct > 0),
  max_gross_exposure_pct  REAL    NOT NULL DEFAULT 100 CHECK (max_gross_exposure_pct > 0),
  max_order_pct           REAL    NOT NULL DEFAULT 100 CHECK (max_order_pct > 0),
  created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_portfolio_risk_project ON portfolio_risk_policies(project_id, risk_level);

CREATE TABLE portfolio_risk_state (
  portfolio_id          INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
  project_id            TEXT    NOT NULL,
  high_water_equity     REAL    NOT NULL,
  current_drawdown_pct  REAL    NOT NULL DEFAULT 0,
  high_water_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE portfolio_objectives (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,
  portfolio_id     INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  name             TEXT    NOT NULL,
  metric           TEXT    NOT NULL
    CHECK (metric IN ('period_return_pct','total_return_pct','day_return_pct','drawdown_pct')),
  target_pct       REAL    NOT NULL,
  direction        TEXT    NOT NULL DEFAULT 'at_least'
    CHECK (direction IN ('at_least','at_most')),
  starts_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deadline_at      TIMESTAMP,
  baseline_equity  REAL,
  status           TEXT    NOT NULL DEFAULT 'active'
    CHECK (status IN ('draft','active','paused','achieved','expired','archived')),
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_portfolio_objectives_active
  ON portfolio_objectives(project_id, portfolio_id, status, deadline_at);
