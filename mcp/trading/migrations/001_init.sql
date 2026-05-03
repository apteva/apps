-- Trading v0.1 — paper-only, deterministic.
--
-- Every table is partitioned by project_id so the same schema serves
-- both `scope: project` (one install per project; project_id is a
-- safety partition) and `scope: global` (one install across projects;
-- project_id is the isolation boundary, the app filters every read by
-- the calling agent's project).

CREATE TABLE portfolios (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  name            TEXT    NOT NULL,
  agent_id        TEXT,                                   -- "apteva-instance:N" — optional binding
  mandate         TEXT    NOT NULL DEFAULT '',
  allowed_classes TEXT    NOT NULL DEFAULT '["equity","etf"]',  -- JSON array
  starting_cash   REAL    NOT NULL,
  cash            REAL    NOT NULL,
  status          TEXT    NOT NULL DEFAULT 'active',      -- active | paused | halted
  mode            TEXT    NOT NULL DEFAULT 'paper',       -- always paper in v0.1
  config_json     TEXT    NOT NULL DEFAULT '{}',          -- per-portfolio overrides (daily_loss_halt_pct, etc.)
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_pf_proj ON portfolios(project_id, status);

-- Maps Apteva instances → portfolios. One agent often manages several;
-- one portfolio can have many observers. portfolio_list() reads this
-- to scope the response to whoever is calling.
CREATE TABLE portfolio_bindings (
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  instance_id  INTEGER NOT NULL,
  role         TEXT    NOT NULL DEFAULT 'manager',        -- manager | observer
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, instance_id)
);

-- One row per (portfolio, symbol[, outcome]) — outcome distinguishes
-- a YES leg from a NO leg on the same polymarket. avg_cost is the
-- weighted average paid for the qty currently open; realized_pnl
-- accumulates as we trim into winners/losers.
CREATE TABLE positions (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  portfolio_id  INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol        TEXT    NOT NULL,
  asset_class   TEXT    NOT NULL,                         -- equity | etf | crypto | polymarket
  outcome       TEXT,                                       -- YES | NO (polymarket only)
  qty           REAL    NOT NULL,
  avg_cost      REAL    NOT NULL,
  realized_pnl  REAL    NOT NULL DEFAULT 0,
  opened_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_position ON positions(portfolio_id, symbol, COALESCE(outcome, ''));
CREATE INDEX ix_pos_proj ON positions(project_id);

CREATE TABLE orders (
  id               TEXT    PRIMARY KEY,                   -- "o-<uuid7>"
  project_id       TEXT    NOT NULL,
  portfolio_id     INTEGER NOT NULL REFERENCES portfolios(id),
  symbol           TEXT    NOT NULL,
  asset_class      TEXT    NOT NULL,
  side             TEXT    NOT NULL,                      -- buy | sell | yes | no
  type             TEXT    NOT NULL,                      -- market | limit | stop
  qty              REAL    NOT NULL,
  filled_qty       REAL    NOT NULL DEFAULT 0,
  avg_fill_price   REAL    NOT NULL DEFAULT 0,
  limit_price      REAL,
  stop_price       REAL,
  tif              TEXT    NOT NULL DEFAULT 'day',        -- day | gtc | ioc
  status           TEXT    NOT NULL,                      -- working | filled | cancelled | rejected
  rationale        TEXT    NOT NULL,
  source           TEXT    NOT NULL,                      -- "agent" | "human" (free-form prefix allowed)
  rejection_code   TEXT,
  rejection_detail TEXT,
  placed_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  resolved_at      TIMESTAMP
);
CREATE INDEX ix_orders_pf_status ON orders(portfolio_id, status, placed_at DESC);
CREATE INDEX ix_orders_proj      ON orders(project_id, status);

CREATE TABLE fills (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  order_id      TEXT    NOT NULL REFERENCES orders(id),
  portfolio_id  INTEGER NOT NULL,
  qty           REAL    NOT NULL,
  price         REAL    NOT NULL,                         -- USD or 0–1 for poly
  fee           REAL    NOT NULL DEFAULT 0,
  filled_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_fills_order ON fills(order_id);
CREATE INDEX ix_fills_pf    ON fills(portfolio_id, filled_at DESC);

-- Append-only journal. Used for thesis notes (agent-written), fill
-- records (engine-written), rationales (auto-attached on order_place),
-- alerts (alert engine), and rejections.
CREATE TABLE journal (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  portfolio_id  INTEGER NOT NULL REFERENCES portfolios(id),
  kind          TEXT    NOT NULL,                         -- thesis | alert | fill | rationale | rejection | note
  body          TEXT    NOT NULL,
  metadata      TEXT    NOT NULL DEFAULT '{}',
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_journal_pf   ON journal(portfolio_id, created_at DESC);
CREATE INDEX ix_journal_kind ON journal(portfolio_id, kind, created_at DESC);

-- Cached marks the engine refreshes each tick. price = USD for
-- equity/etf/crypto, or YES probability (0–1) for polymarket.
CREATE TABLE marks (
  symbol       TEXT    PRIMARY KEY,
  asset_class  TEXT    NOT NULL,
  price        REAL    NOT NULL,
  no_price     REAL,                                       -- polymarket NO; null for others
  prev_close   REAL,
  volume_24h   REAL,
  marked_at    TIMESTAMP NOT NULL
);

CREATE TABLE watchlist (
  project_id   TEXT    NOT NULL,
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol       TEXT    NOT NULL,
  added_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, symbol)
);

-- Alert rules the engine re-evaluates each minute. Single-shot:
-- on match, status flips to 'fired' and the alert engine pushes a
-- SendEvent to the bound instance(s).
CREATE TABLE alerts (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol       TEXT    NOT NULL,
  rule         TEXT    NOT NULL,                          -- mark_above | mark_below | yes_above | yes_below | day_pnl_below | pct_change_above | pct_change_below
  threshold    REAL    NOT NULL,
  status       TEXT    NOT NULL DEFAULT 'active',         -- active | fired | expired
  expires_at   TIMESTAMP,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  fired_at     TIMESTAMP
);
CREATE INDEX ix_alerts_active ON alerts(status, symbol);
CREATE INDEX ix_alerts_pf     ON alerts(portfolio_id, status);

-- Daily P&L baselines. Each portfolio has at most one row per UTC day;
-- mark engine consults this to compute day_pnl = current_equity - baseline.
CREATE TABLE day_baselines (
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  utc_day      TEXT    NOT NULL,                          -- "YYYY-MM-DD"
  equity       REAL    NOT NULL,
  set_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, utc_day)
);
