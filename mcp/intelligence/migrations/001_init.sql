-- market-intel v0.1 — gateway core.
--
-- Partitioned by project_id like every other Apteva app, so the same
-- schema serves scope=project and scope=global installs.

-- Entity resolution cache. "Carlos Alcaraz" has a different external id
-- in api-sports vs tennis-abstract; this table remembers the mapping
-- once it's resolved (LLM-assisted on first encounter) so subsequent
-- lookups skip the resolution step. external_ids is a JSON object:
-- {"api-sports": "12345", "tennis-abstract": "alcaraz_c"}.
CREATE TABLE entities (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  canonical     TEXT    NOT NULL,                  -- normalized display name
  domain        TEXT    NOT NULL,                  -- tennis | nba | nfl | equity | crypto | ...
  external_ids  TEXT    NOT NULL DEFAULT '{}',     -- JSON: {source_slug: external_id}
  aliases       TEXT    NOT NULL DEFAULT '[]',     -- JSON array of alternate names
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_entity ON entities(project_id, domain, canonical);

-- Normalized markets pulled from each prediction-market venue. The
-- venue + external_id is the natural key; we re-upsert prices on every
-- sync. category drives which ground-truth source the gateway routes to.
CREATE TABLE markets (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  venue           TEXT    NOT NULL,                -- polymarket | kalshi | manifold
  external_id     TEXT    NOT NULL,               -- venue-native id (slug / condition_id / ticker)
  question        TEXT    NOT NULL,
  category        TEXT,                            -- sports | macro | crypto | politics | ...
  yes_price       REAL,
  no_price        REAL,
  volume          REAL,
  open_interest   REAL,
  close_time      TIMESTAMP,
  resolved        INTEGER NOT NULL DEFAULT 0,
  resolved_outcome TEXT,
  synced_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_market ON markets(project_id, venue, external_id);
CREATE INDEX ix_market_cat ON markets(project_id, category, resolved);

-- Cached ground-truth probability estimates from non-prediction-market
-- sources. key is source-specific (e.g. "the-odds-api:nfl:KC-SB-winner",
-- "fred:fedfunds-cut-prob"). fair_prob is the de-vigged / derived value.
CREATE TABLE ground_truth (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  source      TEXT    NOT NULL,                    -- the-odds-api | fred | ...
  key         TEXT    NOT NULL,
  fair_prob   REAL,
  raw_payload TEXT,                                -- JSON snapshot of what produced fair_prob
  computed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_ground_truth ON ground_truth(project_id, source, key);

-- Confirmed equivalences between markets (or market ↔ ground-truth).
-- Discovery proposes; operator/agent confirms; the signal scanner only
-- re-prices status=confirmed links.
CREATE TABLE market_links (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  market_a_id  INTEGER NOT NULL,                   -- always a prediction-market row
  market_b_id  INTEGER,                            -- the comparison market (cross-venue), or NULL
  gt_key       TEXT,                               -- ground_truth key (sports/macro), or NULL
  link_type    TEXT    NOT NULL,                   -- cross_venue | ground_truth
  confidence   REAL    NOT NULL DEFAULT 0,
  status       TEXT    NOT NULL DEFAULT 'proposed',-- proposed | confirmed | rejected
  created_by   TEXT,                               -- llm | agent | operator
  notes        TEXT,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_links_status ON market_links(project_id, status);

-- Computed mispricings (signal engine, v0.2). Kept here so the schema
-- is forward-compatible; v0.1 doesn't write to it yet.
CREATE TABLE signals (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  link_id       INTEGER NOT NULL,
  edge_bps      REAL    NOT NULL,
  net_edge_bps  REAL    NOT NULL,
  direction     TEXT    NOT NULL,                  -- SELL_YES | BUY_YES
  suggested_trade TEXT,                            -- JSON
  status        TEXT    NOT NULL DEFAULT 'open',   -- open | acted | stale | resolved
  detected_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_signals_status ON signals(project_id, status, net_edge_bps DESC);

-- Operator-tracked topics/markets to prioritize in sync + discovery.
CREATE TABLE watchlist (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  kind        TEXT    NOT NULL,                    -- topic | market
  value       TEXT    NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_watch ON watchlist(project_id, kind, value);
