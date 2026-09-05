-- Trading v0.6 — provider-neutral security master, corporate actions,
-- exchange sessions, point-in-time universes, and auditable postings.

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
CREATE INDEX ix_securities_status ON securities(asset_class, status);

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
CREATE INDEX ix_security_identifiers_security ON security_identifiers(security_id, identifier_type);

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
CREATE INDEX ix_security_listings_lookup ON security_listings(symbol, venue, valid_from, valid_to);
CREATE INDEX ix_security_listings_security ON security_listings(security_id, valid_from, valid_to);

ALTER TABLE positions ADD COLUMN security_id TEXT REFERENCES securities(id);
ALTER TABLE orders ADD COLUMN security_id TEXT REFERENCES securities(id);
ALTER TABLE marks ADD COLUMN security_id TEXT REFERENCES securities(id);
ALTER TABLE watchlist ADD COLUMN security_id TEXT REFERENCES securities(id);
ALTER TABLE backtest_market_bars ADD COLUMN security_id TEXT REFERENCES securities(id);
CREATE INDEX ix_positions_security ON positions(security_id);
CREATE INDEX ix_orders_security ON orders(security_id);

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
CREATE INDEX ix_corporate_actions_symbol_date ON corporate_actions(symbol, effective_date, ex_date);
CREATE INDEX ix_corporate_actions_security_date ON corporate_actions(security_id, effective_date, ex_date);
CREATE INDEX ix_corporate_actions_type_date ON corporate_actions(action_type, process_date);

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
CREATE INDEX ix_exchange_sessions_date ON exchange_sessions(session_date, venue);

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
CREATE INDEX ix_universe_memberships_point_in_time ON universe_memberships(universe_id, valid_from, valid_to);

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
CREATE INDEX ix_corporate_action_postings_pf ON corporate_action_postings(portfolio_id, applied_at DESC);

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
CREATE INDEX ix_reference_data_issues_status ON reference_data_issues(status, severity, last_seen_at DESC);

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

ALTER TABLE backtest_runs ADD COLUMN adjustment_mode TEXT NOT NULL DEFAULT 'provider_adjusted';
ALTER TABLE backtest_runs ADD COLUMN reference_manifest_json TEXT NOT NULL DEFAULT '{}';
