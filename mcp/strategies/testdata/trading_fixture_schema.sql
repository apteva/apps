-- Trading's account and reference schema as Strategies carried it at
-- migration 027, plus Trading 0.20.0's order revision triggers. Test-only:
-- the fake Trading in trading_fake_test.go serves portfolio_state,
-- fills_feed, research_reference and order_place from a DB with this schema.

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
, broker_slug TEXT, available_cash REAL, execution_environment TEXT NOT NULL DEFAULT 'simulation', live_armed INTEGER NOT NULL DEFAULT 0, broker_binding_required INTEGER NOT NULL DEFAULT 0);
CREATE TABLE portfolio_bindings (
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  instance_id  INTEGER NOT NULL,
  role         TEXT    NOT NULL DEFAULT 'manager',        -- manager | observer
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, instance_id)
);
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
, security_id TEXT REFERENCES securities(id));
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
, outcome TEXT, security_id TEXT REFERENCES securities(id), liquidity_role TEXT NOT NULL DEFAULT 'unknown', broker_order_id TEXT NOT NULL DEFAULT '', broker_connection_id INTEGER NOT NULL DEFAULT 0, cancel_requested INTEGER NOT NULL DEFAULT 0, reconciliation_required INTEGER NOT NULL DEFAULT 0, strategy_id INTEGER NOT NULL DEFAULT 0, strategy_version INTEGER NOT NULL DEFAULT 0, origin_app TEXT NOT NULL DEFAULT '', origin_install_id INTEGER NOT NULL DEFAULT 0, decision_ref TEXT NOT NULL DEFAULT '', external_strategy_id INTEGER NOT NULL DEFAULT 0, external_strategy_version INTEGER NOT NULL DEFAULT 0);
CREATE TABLE fills (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  order_id      TEXT    NOT NULL REFERENCES orders(id),
  portfolio_id  INTEGER NOT NULL,
  qty           REAL    NOT NULL,
  price         REAL    NOT NULL,                         -- USD or 0–1 for poly
  fee           REAL    NOT NULL DEFAULT 0,
  filled_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
, fee_currency TEXT NOT NULL DEFAULT 'USD', liquidity_role TEXT NOT NULL DEFAULT 'unknown', spread_cost REAL NOT NULL DEFAULT 0, slippage_cost REAL NOT NULL DEFAULT 0, venue_slug TEXT NOT NULL DEFAULT 'simulation', fee_source TEXT NOT NULL DEFAULT 'model');
CREATE TABLE journal (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  portfolio_id  INTEGER NOT NULL REFERENCES portfolios(id),
  kind          TEXT    NOT NULL,                         -- thesis | alert | fill | rationale | rejection | note
  body          TEXT    NOT NULL,
  metadata      TEXT    NOT NULL DEFAULT '{}',
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE marks (
  symbol       TEXT    PRIMARY KEY,
  asset_class  TEXT    NOT NULL,
  price        REAL    NOT NULL,
  no_price     REAL,                                       -- polymarket NO; null for others
  prev_close   REAL,
  volume_24h   REAL,
  marked_at    TIMESTAMP NOT NULL
, source TEXT NOT NULL DEFAULT '', timestamp_kind TEXT NOT NULL DEFAULT '', volume_unit TEXT NOT NULL DEFAULT '', received_at TIMESTAMP, bid_price REAL, ask_price REAL, bid_size REAL, ask_size REAL, last_trade_price REAL, last_trade_size REAL, feed TEXT NOT NULL DEFAULT '', quote_at TIMESTAMP, security_id TEXT REFERENCES securities(id));
CREATE TABLE watchlist (
  project_id   TEXT    NOT NULL,
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol       TEXT    NOT NULL,
  added_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP, security_id TEXT REFERENCES securities(id),
  PRIMARY KEY (portfolio_id, symbol)
);
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
CREATE TABLE day_baselines (
  portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  utc_day      TEXT    NOT NULL,                          -- "YYYY-MM-DD"
  equity       REAL    NOT NULL,
  set_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, utc_day)
);
CREATE TABLE position_accounting (
  portfolio_id       INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol             TEXT    NOT NULL,
  outcome            TEXT    NOT NULL DEFAULT '',
  gross_realized_pnl REAL    NOT NULL DEFAULT 0,
  fees_paid          REAL    NOT NULL DEFAULT 0,
  updated_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, symbol, outcome)
);
CREATE TABLE instruments (
  symbol            TEXT PRIMARY KEY,
  provider_symbol   TEXT NOT NULL,
  name              TEXT NOT NULL DEFAULT '',
  asset_class       TEXT NOT NULL,
  exchange          TEXT NOT NULL,
  exchange_timezone TEXT NOT NULL,
  calendar          TEXT NOT NULL,
  base_currency     TEXT NOT NULL DEFAULT '',
  quote_currency    TEXT NOT NULL DEFAULT '',
  volume_unit       TEXT NOT NULL,
  tick_size         REAL NOT NULL DEFAULT 0,
  lot_size          REAL NOT NULL DEFAULT 0,
  active            INTEGER NOT NULL DEFAULT 1,
  expires_at        TIMESTAMP,
  source            TEXT NOT NULL,
  updated_at        TIMESTAMP NOT NULL
, min_qty REAL NOT NULL DEFAULT 0, min_notional REAL NOT NULL DEFAULT 0);
CREATE TABLE market_bars (
  symbol          TEXT NOT NULL,
  timeframe       TEXT NOT NULL,
  bar_at          TIMESTAMP NOT NULL,
  open            REAL NOT NULL,
  high            REAL NOT NULL,
  low             REAL NOT NULL,
  close           REAL NOT NULL,
  volume          REAL NOT NULL DEFAULT 0,
  trade_count     INTEGER NOT NULL DEFAULT 0,
  vwap            REAL NOT NULL DEFAULT 0,
  source          TEXT NOT NULL,
  feed            TEXT NOT NULL DEFAULT '',
  received_at     TIMESTAMP NOT NULL,
  complete        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (symbol, timeframe, bar_at, source, feed)
);
CREATE TABLE securities (
  id               TEXT PRIMARY KEY,
  asset_class      TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'active',
  primary_currency TEXT NOT NULL DEFAULT '',
  source           TEXT NOT NULL,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE security_identifiers (
  security_id      TEXT NOT NULL REFERENCES securities(id),
  identifier_type  TEXT NOT NULL,
  identifier_value TEXT NOT NULL,
  valid_from       TEXT NOT NULL DEFAULT '',
  valid_to         TEXT NOT NULL DEFAULT '',
  source           TEXT NOT NULL,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (identifier_type, identifier_value, valid_from, source)
);
CREATE TABLE security_listings (
  id                 INTEGER PRIMARY KEY,
  security_id        TEXT NOT NULL REFERENCES securities(id),
  provider_asset_id  TEXT NOT NULL DEFAULT '',
  venue              TEXT NOT NULL,
  symbol             TEXT NOT NULL,
  currency           TEXT NOT NULL DEFAULT '',
  valid_from         TEXT NOT NULL DEFAULT '',
  valid_to           TEXT NOT NULL DEFAULT '',
  active             INTEGER NOT NULL DEFAULT 1,
  source             TEXT NOT NULL,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (venue, symbol, valid_from, source)
);
CREATE TABLE corporate_actions (
  provider             TEXT NOT NULL,
  provider_event_id    TEXT NOT NULL,
  revision             INTEGER NOT NULL DEFAULT 1,
  action_type          TEXT NOT NULL,
  status               TEXT NOT NULL DEFAULT 'confirmed',
  security_id          TEXT REFERENCES securities(id),
  related_security_id  TEXT REFERENCES securities(id),
  symbol               TEXT NOT NULL DEFAULT '',
  new_symbol           TEXT NOT NULL DEFAULT '',
  cusip                 TEXT NOT NULL DEFAULT '',
  isin                  TEXT NOT NULL DEFAULT '',
  announcement_date     TEXT NOT NULL DEFAULT '',
  ex_date               TEXT NOT NULL DEFAULT '',
  record_date           TEXT NOT NULL DEFAULT '',
  payable_date          TEXT NOT NULL DEFAULT '',
  effective_date        TEXT NOT NULL DEFAULT '',
  process_date          TEXT NOT NULL DEFAULT '',
  ratio_numerator       REAL NOT NULL DEFAULT 0,
  ratio_denominator     REAL NOT NULL DEFAULT 0,
  cash_amount           REAL NOT NULL DEFAULT 0,
  currency              TEXT NOT NULL DEFAULT '',
  data_quality          TEXT NOT NULL DEFAULT 'complete',
  raw_json              TEXT NOT NULL,
  payload_sha256        TEXT NOT NULL,
  ingested_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (provider, provider_event_id, revision)
);
CREATE TABLE exchange_sessions (
  venue          TEXT NOT NULL,
  session_date   TEXT NOT NULL,
  session_type   TEXT NOT NULL DEFAULT 'regular',
  open_at        TEXT NOT NULL DEFAULT '',
  close_at       TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'open',
  source         TEXT NOT NULL,
  revision       INTEGER NOT NULL DEFAULT 1,
  updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (venue, session_date, session_type, source)
);
CREATE TABLE universe_memberships (
  universe_id  TEXT NOT NULL,
  security_id  TEXT NOT NULL REFERENCES securities(id),
  valid_from   TEXT NOT NULL,
  valid_to     TEXT NOT NULL DEFAULT '',
  source       TEXT NOT NULL,
  revision     INTEGER NOT NULL DEFAULT 1,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (universe_id, security_id, valid_from, source)
);
CREATE TABLE corporate_action_postings (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  portfolio_id          INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  provider              TEXT NOT NULL,
  provider_event_id     TEXT NOT NULL,
  provider_revision     INTEGER NOT NULL,
  effect_type           TEXT NOT NULL,
  security_id           TEXT,
  related_security_id   TEXT,
  symbol                TEXT NOT NULL DEFAULT '',
  related_symbol        TEXT NOT NULL DEFAULT '',
  quantity_delta        REAL NOT NULL DEFAULT 0,
  cash_delta            REAL NOT NULL DEFAULT 0,
  cost_basis_delta      REAL NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'applied',
  details_json          TEXT NOT NULL DEFAULT '{}',
  applied_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (portfolio_id, provider, provider_event_id, provider_revision, effect_type, symbol, related_symbol)
);
CREATE TABLE corporate_action_entitlements (
  portfolio_id        INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  provider            TEXT NOT NULL,
  provider_event_id   TEXT NOT NULL,
  provider_revision   INTEGER NOT NULL,
  symbol              TEXT NOT NULL,
  entitled_quantity   REAL NOT NULL,
  captured_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, provider, provider_event_id, provider_revision, symbol)
);
CREATE TABLE reference_data_issues (
  id                INTEGER PRIMARY KEY,
  provider          TEXT NOT NULL,
  issue_key         TEXT NOT NULL,
  severity          TEXT NOT NULL,
  category          TEXT NOT NULL,
  message           TEXT NOT NULL,
  payload_json      TEXT NOT NULL DEFAULT '{}',
  status            TEXT NOT NULL DEFAULT 'open',
  first_seen_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at       TIMESTAMP,
  UNIQUE(provider, issue_key)
);
CREATE TABLE reference_data_checkpoints (
  provider       TEXT NOT NULL,
  stream         TEXT NOT NULL,
  cursor         TEXT NOT NULL DEFAULT '',
  watermark      TEXT NOT NULL DEFAULT '',
  last_ok_at     TIMESTAMP,
  last_error_at  TIMESTAMP,
  last_error     TEXT NOT NULL DEFAULT '',
  rows_ingested  INTEGER NOT NULL DEFAULT 0,
  updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(provider, stream)
);
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
CREATE TABLE venue_execution_profiles (
  venue_slug              TEXT    NOT NULL,
  asset_class             TEXT    NOT NULL,
  symbol                  TEXT    NOT NULL DEFAULT '*',
  status                  TEXT    NOT NULL DEFAULT 'active',
  calendar                TEXT    NOT NULL DEFAULT '24X7',
  session_policy          TEXT    NOT NULL DEFAULT 'continuous',
  maker_fee_bps           REAL    NOT NULL DEFAULT 0,
  taker_fee_bps           REAL    NOT NULL DEFAULT 0,
  fee_currency            TEXT    NOT NULL DEFAULT 'USD',
  spread_model            TEXT    NOT NULL DEFAULT 'quote',
  fallback_spread_bps     REAL    NOT NULL DEFAULT 0,
  slippage_model          TEXT    NOT NULL DEFAULT 'fixed_bps',
  slippage_bps            REAL    NOT NULL DEFAULT 1,
  min_qty                 REAL    NOT NULL DEFAULT 0,
  min_notional            REAL    NOT NULL DEFAULT 0,
  qty_step                REAL    NOT NULL DEFAULT 0,
  price_tick              REAL    NOT NULL DEFAULT 0,
  funding_rate_bps        REAL    NOT NULL DEFAULT 0,
  funding_interval_hours  INTEGER NOT NULL DEFAULT 0,
  supports_post_only      INTEGER NOT NULL DEFAULT 0,
  supports_reduce_only    INTEGER NOT NULL DEFAULT 0,
  source                  TEXT    NOT NULL DEFAULT 'operator',
  updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (venue_slug, asset_class, symbol),
  CHECK (status IN ('active','degraded','maintenance','outage')),
  CHECK (session_policy IN ('continuous','regular_only','venue_managed')),
  CHECK (spread_model IN ('quote','fixed_bps','none')),
  CHECK (slippage_model IN ('fixed_bps','none'))
);
CREATE TABLE execution_costs (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  portfolio_id    INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  order_id        TEXT,
  fill_id         INTEGER,
  venue_slug      TEXT    NOT NULL,
  symbol          TEXT    NOT NULL,
  kind            TEXT    NOT NULL,
  amount          REAL    NOT NULL,
  currency        TEXT    NOT NULL,
  rate_bps        REAL,
  liquidity_role  TEXT,
  provider_event_id TEXT,
  metadata        TEXT    NOT NULL DEFAULT '{}',
  occurred_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('fee','spread','slippage','funding','rebate'))
);
CREATE TABLE portfolio_universe_policies (
  portfolio_id          INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
  project_id            TEXT    NOT NULL,
  selection_mode        TEXT    NOT NULL DEFAULT 'all_allowed_classes'
    CHECK (selection_mode IN ('all_allowed_classes','symbol_allowlist','reference_universe')),
  include_symbols       TEXT    NOT NULL DEFAULT '[]',
  exclude_symbols       TEXT    NOT NULL DEFAULT '[]',
  reference_universe_id TEXT    NOT NULL DEFAULT '',
  require_active_listing INTEGER NOT NULL DEFAULT 0 CHECK (require_active_listing IN (0,1)),
  enforcement_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (enforcement_enabled IN (0,1)),
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE broker_bindings (
 portfolio_id INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
 connection_id INTEGER NOT NULL,
 execution_environment TEXT NOT NULL,
 UNIQUE(connection_id)
);
CREATE TABLE order_requests (
 project_id TEXT NOT NULL, portfolio_id INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL, intent_hash TEXT NOT NULL, order_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(project_id,portfolio_id,request_key)
);
CREATE TABLE objective_results (
 objective_id INTEGER PRIMARY KEY REFERENCES portfolio_objectives(id) ON DELETE CASCADE,
 actual_pct REAL NOT NULL, achieved INTEGER NOT NULL, finalized_at TEXT NOT NULL
);
CREATE TABLE portfolio_revisions (portfolio_id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0);
CREATE TABLE position_history (id INTEGER PRIMARY KEY, portfolio_id INTEGER NOT NULL, symbol TEXT NOT NULL, outcome TEXT NOT NULL, qty REAL NOT NULL, avg_cost REAL NOT NULL, observed_at TEXT NOT NULL);
CREATE TABLE objective_observations (objective_id INTEGER NOT NULL REFERENCES portfolio_objectives(id) ON DELETE CASCADE, observed_at TEXT NOT NULL, actual_pct REAL NOT NULL, PRIMARY KEY(objective_id,observed_at));
CREATE TABLE order_commissions (order_id TEXT NOT NULL REFERENCES orders(id), currency TEXT NOT NULL, amount REAL NOT NULL, PRIMARY KEY(order_id,currency));
CREATE TABLE replay_steps (portfolio_id INTEGER PRIMARY KEY REFERENCES portfolios(id), run_id INTEGER NOT NULL, step INTEGER NOT NULL, digest TEXT NOT NULL, replay_at TEXT NOT NULL);
CREATE INDEX ix_pf_proj ON portfolios(project_id, status);
CREATE UNIQUE INDEX ux_position ON positions(portfolio_id, symbol, COALESCE(outcome, ''));
CREATE INDEX ix_pos_proj ON positions(project_id);
CREATE INDEX ix_orders_pf_status ON orders(portfolio_id, status, placed_at DESC);
CREATE INDEX ix_orders_proj      ON orders(project_id, status);
CREATE INDEX ix_fills_order ON fills(order_id);
CREATE INDEX ix_fills_pf    ON fills(portfolio_id, filled_at DESC);
CREATE INDEX ix_journal_pf   ON journal(portfolio_id, created_at DESC);
CREATE INDEX ix_journal_kind ON journal(portfolio_id, kind, created_at DESC);
CREATE INDEX ix_alerts_active ON alerts(status, symbol);
CREATE INDEX ix_alerts_pf     ON alerts(portfolio_id, status);
CREATE INDEX ix_pf_broker ON portfolios(broker_slug);
CREATE INDEX ix_position_accounting_pf ON position_accounting(portfolio_id);
CREATE INDEX ix_instruments_asset_exchange ON instruments(asset_class, exchange);
CREATE INDEX ix_market_bars_lookup ON market_bars(symbol, timeframe, bar_at DESC);
CREATE INDEX ix_securities_status ON securities(asset_class, status);
CREATE INDEX ix_security_identifiers_security ON security_identifiers(security_id, identifier_type);
CREATE INDEX ix_security_listings_lookup ON security_listings(symbol, venue, valid_from, valid_to);
CREATE INDEX ix_security_listings_security ON security_listings(security_id, valid_from, valid_to);
CREATE INDEX ix_positions_security ON positions(security_id);
CREATE INDEX ix_orders_security ON orders(security_id);
CREATE INDEX ix_corporate_actions_symbol_date ON corporate_actions(symbol, effective_date, ex_date);
CREATE INDEX ix_corporate_actions_security_date ON corporate_actions(security_id, effective_date, ex_date);
CREATE INDEX ix_corporate_actions_type_date ON corporate_actions(action_type, process_date);
CREATE INDEX ix_exchange_sessions_date ON exchange_sessions(session_date, venue);
CREATE INDEX ix_universe_memberships_point_in_time ON universe_memberships(universe_id, valid_from, valid_to);
CREATE INDEX ix_corporate_action_postings_pf ON corporate_action_postings(portfolio_id, applied_at DESC);
CREATE INDEX ix_reference_data_issues_status ON reference_data_issues(status, severity, last_seen_at DESC);
CREATE INDEX ix_portfolio_risk_project ON portfolio_risk_policies(project_id, risk_level);
CREATE INDEX ix_portfolio_objectives_active
  ON portfolio_objectives(project_id, portfolio_id, status, deadline_at);
CREATE INDEX ix_execution_costs_pf ON execution_costs(portfolio_id, occurred_at DESC);
CREATE INDEX ix_execution_costs_order ON execution_costs(order_id);
CREATE UNIQUE INDEX ux_execution_cost_event
  ON execution_costs(portfolio_id, venue_slug, kind, provider_event_id)
  WHERE provider_event_id IS NOT NULL AND provider_event_id != '';
CREATE INDEX ix_portfolio_universe_project
  ON portfolio_universe_policies(project_id, selection_mode);
CREATE INDEX ix_orders_broker_id ON orders(broker_connection_id,broker_order_id);
CREATE INDEX ix_position_history_at ON position_history(portfolio_id,symbol,outcome,observed_at,id);
CREATE TRIGGER history_position_insert AFTER INSERT ON positions BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(NEW.portfolio_id,NEW.symbol,COALESCE(NEW.outcome,''),NEW.qty,NEW.avg_cost,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER history_position_update AFTER UPDATE OF qty,avg_cost,symbol,outcome ON positions
WHEN OLD.qty IS NOT NEW.qty OR OLD.avg_cost IS NOT NEW.avg_cost OR OLD.symbol IS NOT NEW.symbol OR OLD.outcome IS NOT NEW.outcome BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at)
 SELECT OLD.portfolio_id,OLD.symbol,COALESCE(OLD.outcome,''),0,0,strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE OLD.symbol IS NOT NEW.symbol OR COALESCE(OLD.outcome,'') IS NOT COALESCE(NEW.outcome,'');
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(NEW.portfolio_id,NEW.symbol,COALESCE(NEW.outcome,''),NEW.qty,NEW.avg_cost,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER history_position_delete AFTER DELETE ON positions BEGIN
 INSERT INTO position_history(portfolio_id,symbol,outcome,qty,avg_cost,observed_at) VALUES(OLD.portfolio_id,OLD.symbol,COALESCE(OLD.outcome,''),0,0,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 INSERT INTO portfolio_revisions VALUES(OLD.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER revision_cash AFTER UPDATE OF cash ON portfolios WHEN OLD.cash IS NOT NEW.cash BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER revision_order_insert AFTER INSERT ON orders BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER revision_order_status AFTER UPDATE OF status,filled_qty ON orders
WHEN OLD.status IS NOT NEW.status OR OLD.filled_qty IS NOT NEW.filled_qty BEGIN
 INSERT INTO portfolio_revisions VALUES(NEW.portfolio_id,1) ON CONFLICT(portfolio_id) DO UPDATE SET revision=revision+1;
END;
