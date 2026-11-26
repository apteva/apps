-- arena v0.1: live game matches for agents, bots and humans.
--
-- A match stores its game state as a JSON snapshot plus an append-only
-- event log. The seed that drives shuffles is kept secret (only its
-- SHA-256 is shown) until the match ends, then revealed for audit.

CREATE TABLE IF NOT EXISTS matches (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    game          TEXT    NOT NULL,
    title         TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'waiting'
                  CHECK(status IN ('waiting','live','finished','cancelled')),
    options_json  TEXT    NOT NULL DEFAULT '{}',
    seat_count    INTEGER NOT NULL,
    turn_seconds  INTEGER NOT NULL DEFAULT 0,
    seed_hex      TEXT    NOT NULL,
    seed_hash     TEXT    NOT NULL,
    state_json    TEXT    NOT NULL DEFAULT '',
    version       INTEGER NOT NULL DEFAULT 0,
    result_json   TEXT    NOT NULL DEFAULT '',
    turn_deadline TEXT    NOT NULL DEFAULT '',
    created_by    TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    started_at    TEXT    NOT NULL DEFAULT '',
    finished_at   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_matches_project_status
    ON matches(project_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS seats (
    match_id    INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    seat        INTEGER NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('open','agent','bot','human')),
    agent_id    INTEGER NOT NULL DEFAULT 0,
    name        TEXT    NOT NULL DEFAULT '',
    bot_level   TEXT    NOT NULL DEFAULT '',
    token_hash  TEXT    NOT NULL DEFAULT '',
    joined_at   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (match_id, seat)
);

CREATE INDEX IF NOT EXISTS idx_seats_agent ON seats(agent_id, match_id);

CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    match_id        INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    seat            INTEGER,
    type            TEXT    NOT NULL,
    data_json       TEXT    NOT NULL DEFAULT '{}',
    private_to_json TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_match ON events(match_id, id);
