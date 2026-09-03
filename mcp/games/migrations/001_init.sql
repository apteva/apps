-- 001_init — Games v0.1: players and progression.
--
-- Identity lives in the Auth app; players.auth_user_id is the join key.
-- Every table carries project_id (one project = one game title).
-- Writes that must be atomic (a data write with an expected version,
-- a stat update with its leaderboard entries) run in one transaction.

CREATE TABLE settings (
  project_id  TEXT NOT NULL,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (project_id, key)
);

CREATE TABLE players (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  auth_user_id    INTEGER NOT NULL,
  display_name    TEXT    NOT NULL DEFAULT '',
  avatar_url      TEXT,
  region          TEXT,
  locale          TEXT,
  metadata_json   TEXT    NOT NULL DEFAULT '{}',        -- public profile metadata
  status          TEXT    NOT NULL DEFAULT 'active',    -- active | banned
  kind            TEXT    NOT NULL DEFAULT 'guest',     -- Auth user kind at last login: guest | account
  login_count     INTEGER NOT NULL DEFAULT 0,
  first_login_at  TEXT,
  last_login_at   TEXT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_players_auth       ON players(project_id, auth_user_id);
CREATE        INDEX ix_players_name       ON players(project_id, display_name);
CREATE        INDEX ix_players_status     ON players(project_id, status);
CREATE        INDEX ix_players_last_login ON players(project_id, last_login_at DESC);

CREATE TABLE player_bans (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  player_id   INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  reason      TEXT,
  source      TEXT,                                     -- agent | dashboard | api
  expires_at  TEXT,                                     -- NULL = permanent
  lifted_at   TEXT,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_bans_player ON player_bans(player_id, lifted_at);

-- Append-only per-player trail: logins, bans, stat writes, erasures.
CREATE TABLE player_audit (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  player_id    INTEGER NOT NULL,
  event        TEXT    NOT NULL,
  source       TEXT,
  metadata     TEXT,
  occurred_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_audit_player ON player_audit(player_id, occurred_at DESC);

-- Cloud save. One JSON value per key with an optimistic version.
-- visibility: public (any logged-in player may read), private (owner
-- only), server (tools and /s2s only — never returned to clients).
CREATE TABLE player_data (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  player_id   INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  key         TEXT    NOT NULL,
  value       TEXT    NOT NULL,
  visibility  TEXT    NOT NULL DEFAULT 'private',
  version     INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_data_key ON player_data(player_id, key);

-- Statistic definitions decide how writes fold into the stored value
-- and whether a game client may write the stat at all.
CREATE TABLE stat_defs (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,
  name             TEXT    NOT NULL,
  aggregation      TEXT    NOT NULL DEFAULT 'last',    -- last | max | min | sum
  client_writable  INTEGER NOT NULL DEFAULT 0,
  description      TEXT,
  created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_stat_defs ON stat_defs(project_id, name);

CREATE TABLE player_stats (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  player_id   INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  stat        TEXT    NOT NULL,
  value       REAL    NOT NULL DEFAULT 0,
  version     INTEGER NOT NULL DEFAULT 1,
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_player_stats ON player_stats(player_id, stat);
CREATE        INDEX ix_stats_value  ON player_stats(project_id, stat, value DESC);

-- A leaderboard is a view over one statistic. Entries are keyed by
-- period so history stays readable after a reset.
CREATE TABLE leaderboards (
  id                 INTEGER PRIMARY KEY,
  project_id         TEXT    NOT NULL,
  name               TEXT    NOT NULL,
  display_name       TEXT,
  stat               TEXT    NOT NULL,
  sort               TEXT    NOT NULL DEFAULT 'desc',   -- desc | asc
  reset              TEXT    NOT NULL DEFAULT 'none',   -- none | daily | weekly | monthly | season
  season_days        INTEGER NOT NULL DEFAULT 0,
  current_period     TEXT    NOT NULL DEFAULT 'all',
  period_started_at  TEXT,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_leaderboards_name ON leaderboards(project_id, name);
CREATE        INDEX ix_leaderboards_stat ON leaderboards(project_id, stat);

CREATE TABLE leaderboard_entries (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  leaderboard_id  INTEGER NOT NULL REFERENCES leaderboards(id) ON DELETE CASCADE,
  period          TEXT    NOT NULL,
  player_id       INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  score           REAL    NOT NULL,
  updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_lb_entry ON leaderboard_entries(leaderboard_id, period, player_id);
CREATE        INDEX ix_lb_rank  ON leaderboard_entries(leaderboard_id, period, score DESC, updated_at ASC);

CREATE TABLE achievement_defs (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  key         TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT,
  stat        TEXT,                                     -- NULL = manual grant only
  threshold   REAL,
  op          TEXT    NOT NULL DEFAULT 'gte',           -- gte | gt | lte | lt | eq
  hidden      INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_ach_key  ON achievement_defs(project_id, key);
CREATE        INDEX ix_ach_stat ON achievement_defs(project_id, stat);

CREATE TABLE player_achievements (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  player_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  key          TEXT    NOT NULL,
  source       TEXT,
  unlocked_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_player_ach ON player_achievements(player_id, key);
