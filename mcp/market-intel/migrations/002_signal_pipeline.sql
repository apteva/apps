-- Product-grade signal pipeline. Kept separate from the v0.1 placeholder
-- `signals` table so existing installs migrate without destructive rewrites.

CREATE TABLE signal_feeds (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  slug                  TEXT NOT NULL,
  name                  TEXT NOT NULL,
  description           TEXT NOT NULL DEFAULT '',
  asset_class           TEXT NOT NULL DEFAULT 'crypto',
  interval              TEXT NOT NULL DEFAULT '1h',
  min_confidence        REAL NOT NULL DEFAULT 0.62,
  min_net_edge_bps      REAL NOT NULL DEFAULT 20,
  visibility            TEXT NOT NULL DEFAULT 'private', -- private | delayed | premium
  delay_seconds         INTEGER NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'active',
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_signal_feed ON signal_feeds(project_id, slug);

CREATE TABLE signal_runs (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  provider              TEXT NOT NULL,
  feed_slug             TEXT NOT NULL,
  started_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at           TIMESTAMP,
  universe_size         INTEGER NOT NULL DEFAULT 0,
  scanned               INTEGER NOT NULL DEFAULT 0,
  emitted               INTEGER NOT NULL DEFAULT 0,
  rejected              INTEGER NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'running',
  error                 TEXT
);
CREATE INDEX ix_signal_runs_recent ON signal_runs(project_id, started_at DESC);

CREATE TABLE signal_opportunities (
  id                    INTEGER PRIMARY KEY,
  public_id             TEXT NOT NULL,
  project_id            TEXT NOT NULL,
  feed_id               INTEGER NOT NULL,
  run_id                INTEGER,
  provider              TEXT NOT NULL,
  venue                 TEXT NOT NULL,
  provider_symbol       TEXT NOT NULL,
  canonical_symbol      TEXT NOT NULL,
  asset_class           TEXT NOT NULL,
  strategy              TEXT NOT NULL,
  direction             TEXT NOT NULL, -- BUY | SELL
  interval              TEXT NOT NULL,
  signal_time           INTEGER NOT NULL,
  generated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  valid_until           TIMESTAMP NOT NULL,
  entry_price           REAL NOT NULL,
  bid_price             REAL NOT NULL,
  ask_price             REAL NOT NULL,
  stop_loss             REAL NOT NULL,
  target_1              REAL NOT NULL,
  target_2              REAL NOT NULL,
  confidence            REAL NOT NULL,
  score                 REAL NOT NULL,
  expected_move_bps     REAL NOT NULL,
  gross_edge_bps        REAL NOT NULL,
  cost_bps              REAL NOT NULL,
  net_edge_bps          REAL NOT NULL,
  spread_bps            REAL NOT NULL,
  quote_volume_24h      REAL NOT NULL,
  rationale             TEXT NOT NULL DEFAULT '[]',
  features              TEXT NOT NULL DEFAULT '{}',
  provenance            TEXT NOT NULL DEFAULT '{}',
  status                TEXT NOT NULL DEFAULT 'open', -- open | expired | evaluated | withdrawn
  evaluation_price      REAL,
  realized_return_bps   REAL,
  outcome               TEXT, -- win | loss | flat
  evaluated_at          TIMESTAMP,
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_signal_public_id ON signal_opportunities(public_id);
CREATE UNIQUE INDEX ux_signal_dedupe ON signal_opportunities(
  project_id, provider, provider_symbol, strategy, direction, interval, signal_time
);
CREATE INDEX ix_signal_feed_recent ON signal_opportunities(project_id, feed_id, generated_at DESC);
CREATE INDEX ix_signal_open ON signal_opportunities(project_id, status, valid_until);
CREATE INDEX ix_signal_quality ON signal_opportunities(project_id, confidence DESC, net_edge_bps DESC);

CREATE TABLE provider_health (
  project_id            TEXT NOT NULL,
  provider              TEXT NOT NULL,
  status                TEXT NOT NULL,
  latency_ms            INTEGER NOT NULL DEFAULT 0,
  instruments           INTEGER NOT NULL DEFAULT 0,
  last_success_at       TIMESTAMP,
  last_error_at         TIMESTAMP,
  error                 TEXT,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, provider)
);

