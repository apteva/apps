-- robot: agent navigation eval episodes.
--
-- Each episode is one attempt at one scenario by one agent. Episodes
-- never resume — abandoned episodes stay open until a sidecar restart
-- garbage-collects them (or until manually closed via the panel).

CREATE TABLE IF NOT EXISTS robot_episodes (
    id              TEXT    PRIMARY KEY,
    scenario_id     TEXT    NOT NULL,
    model           TEXT    NOT NULL DEFAULT '',
    started_at      DATETIME NOT NULL,
    ended_at        DATETIME,
    success         INTEGER  NOT NULL DEFAULT 0
                    CHECK(success IN (0,1)),
    steps           INTEGER  NOT NULL DEFAULT 0,
    optimal_steps   INTEGER  NOT NULL DEFAULT 0,
    max_steps       INTEGER  NOT NULL,
    terminal_reason TEXT     NOT NULL DEFAULT ''
                    CHECK(terminal_reason IN ('','success','timeout')),
    walltime_ms     INTEGER  NOT NULL DEFAULT 0,
    -- live state for active episodes — final values when ended.
    pos_x           INTEGER  NOT NULL,
    pos_y           INTEGER  NOT NULL,
    heading         TEXT     NOT NULL DEFAULT 'N'
                    CHECK(heading IN ('N','E','S','W'))
);

CREATE INDEX IF NOT EXISTS idx_robot_episodes_active
    ON robot_episodes(ended_at)
    WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_robot_episodes_scenario
    ON robot_episodes(scenario_id);

CREATE TABLE IF NOT EXISTS robot_episode_steps (
    episode_id  TEXT    NOT NULL REFERENCES robot_episodes(id) ON DELETE CASCADE,
    step        INTEGER NOT NULL,
    tool        TEXT    NOT NULL,
    args_json   TEXT    NOT NULL DEFAULT '{}',
    result_json TEXT    NOT NULL DEFAULT '{}',
    pos_x       INTEGER NOT NULL,
    pos_y       INTEGER NOT NULL,
    walltime_ms INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (episode_id, step)
);
