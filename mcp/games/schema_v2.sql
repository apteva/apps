CREATE TABLE settings_v2 (
  project_id  TEXT NOT NULL,
  game_id TEXT NOT NULL,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (project_id, game_id, key),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id)
);
CREATE TABLE players_v2 (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  auth_user_id    INTEGER NOT NULL,
  display_name    TEXT    NOT NULL DEFAULT '',
  avatar_url      TEXT,
  region          TEXT,
  locale          TEXT,
  metadata_json   TEXT    NOT NULL DEFAULT '{}',
  status          TEXT    NOT NULL DEFAULT 'active',
  kind            TEXT    NOT NULL DEFAULT 'guest',
  login_count     INTEGER NOT NULL DEFAULT 0,
  first_login_at  TEXT,
  last_login_at   TEXT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  UNIQUE(project_id, game_id, id)
);
CREATE TABLE player_bans_v2 (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  player_id   INTEGER NOT NULL,
  reason      TEXT,
  source      TEXT,
  expires_at  TEXT,
  lifted_at   TEXT,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE TABLE player_audit_v2 (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  player_id    INTEGER NOT NULL,
  event        TEXT    NOT NULL,
  source       TEXT,
  metadata     TEXT,
  occurred_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE TABLE player_data_v2 (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  player_id   INTEGER NOT NULL,
  key         TEXT    NOT NULL,
  value       TEXT    NOT NULL,
  visibility  TEXT    NOT NULL DEFAULT 'private',
  version     INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE TABLE stat_defs_v2 (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  name             TEXT    NOT NULL,
  aggregation      TEXT    NOT NULL DEFAULT 'last',
  client_writable  INTEGER NOT NULL DEFAULT 0,
  description      TEXT,
  created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id)
);
CREATE TABLE player_stats_v2 (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  player_id   INTEGER NOT NULL,
  stat        TEXT    NOT NULL,
  value       REAL    NOT NULL DEFAULT 0,
  version     INTEGER NOT NULL DEFAULT 1,
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE TABLE leaderboards_v2 (
  id                 INTEGER PRIMARY KEY,
  project_id         TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  name               TEXT    NOT NULL,
  display_name       TEXT,
  stat               TEXT    NOT NULL,
  sort               TEXT    NOT NULL DEFAULT 'desc',
  reset              TEXT    NOT NULL DEFAULT 'none',
  season_days        INTEGER NOT NULL DEFAULT 0,
  current_period     TEXT    NOT NULL DEFAULT 'all',
  period_started_at  TEXT,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  UNIQUE(project_id, game_id, id)
);
CREATE TABLE leaderboard_entries_v2 (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  leaderboard_id  INTEGER NOT NULL,
  period          TEXT    NOT NULL,
  player_id       INTEGER NOT NULL,
  score           REAL    NOT NULL,
  updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE,
  FOREIGN KEY(project_id, game_id, leaderboard_id) REFERENCES leaderboards_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE TABLE achievement_defs_v2 (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  key         TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT,
  stat        TEXT,
  threshold   REAL,
  op          TEXT    NOT NULL DEFAULT 'gte',
  hidden      INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id)
);
CREATE TABLE player_achievements_v2 (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  game_id TEXT NOT NULL,
  player_id    INTEGER NOT NULL,
  key          TEXT    NOT NULL,
  source       TEXT,
  unlocked_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (project_id, game_id) REFERENCES games(project_id, id),
  FOREIGN KEY(project_id, game_id, player_id) REFERENCES players_v2(project_id, game_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX v2_ux_players_auth       ON players_v2(project_id, game_id, auth_user_id);
CREATE        INDEX v2_ix_players_name       ON players_v2(project_id, game_id, display_name);
CREATE        INDEX v2_ix_players_status     ON players_v2(project_id, game_id, status);
CREATE        INDEX v2_ix_players_last_login ON players_v2(project_id, game_id, last_login_at DESC);
CREATE INDEX v2_ix_bans_player ON player_bans_v2(player_id, lifted_at);
CREATE INDEX v2_ix_audit_player ON player_audit_v2(player_id, occurred_at DESC);
CREATE UNIQUE INDEX v2_ux_data_key ON player_data_v2(player_id, key);
CREATE UNIQUE INDEX v2_ux_stat_defs ON stat_defs_v2(project_id, game_id, name);
CREATE UNIQUE INDEX v2_ux_player_stats ON player_stats_v2(player_id, stat);
CREATE        INDEX v2_ix_stats_value  ON player_stats_v2(project_id, game_id, stat, value DESC);
CREATE UNIQUE INDEX v2_ux_leaderboards_name ON leaderboards_v2(project_id, game_id, name);
CREATE        INDEX v2_ix_leaderboards_stat ON leaderboards_v2(project_id, game_id, stat);
CREATE UNIQUE INDEX v2_ux_lb_entry ON leaderboard_entries_v2(leaderboard_id, period, player_id);
CREATE        INDEX v2_ix_lb_rank  ON leaderboard_entries_v2(leaderboard_id, period, score DESC, updated_at ASC);
CREATE UNIQUE INDEX v2_ux_ach_key  ON achievement_defs_v2(project_id, game_id, key);
CREATE        INDEX v2_ix_ach_stat ON achievement_defs_v2(project_id, game_id, stat);
CREATE UNIQUE INDEX v2_ux_player_ach ON player_achievements_v2(player_id, key);
CREATE INDEX v2_lb_asc ON leaderboard_entries_v2(project_id, game_id, leaderboard_id, period, score ASC, updated_at ASC, player_id ASC);
CREATE INDEX v2_lb_desc ON leaderboard_entries_v2(project_id, game_id, leaderboard_id, period, score DESC, updated_at ASC, player_id ASC);
