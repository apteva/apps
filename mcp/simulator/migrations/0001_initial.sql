-- Apteva Simulator v0.1 — device + run state.
--
-- sims:        one row per AVD (android) or Simulator device (ios) this
--              app has booted at least once. Survives sim shutdown so
--              re-booting the same device skips create. id is the AVD
--              name on android or the simctl UDID on ios.
-- sim_runs:    one row per build+install+launch cycle. Append-only;
--              latest row per sim is "the current run".
-- sim_streams: one row per active live-stream WS session. ws_token is
--              the short-lived bearer (sims_stream_url mints it,
--              stream.go validates on WS upgrade).

CREATE TABLE sims (
  id           TEXT    PRIMARY KEY,           -- AVD name (android) or simctl UDID (ios)
  project_id   TEXT    NOT NULL,
  platform     TEXT    NOT NULL,              -- 'android' | 'ios'
  runtime      TEXT    NOT NULL DEFAULT '',   -- android system-image or ios runtime id
  device_type  TEXT    NOT NULL DEFAULT '',   -- pixel_6 | iPhone-15-Pro etc
  status       TEXT    NOT NULL DEFAULT 'shutdown',  -- shutdown | booting | booted | crashed
  pid          INTEGER NOT NULL DEFAULT 0,    -- emulator pid (android); 0 for ios (host-managed)
  serial       TEXT    NOT NULL DEFAULT '',   -- adb serial like emulator-5554 (android only)
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  booted_at    TIMESTAMP,
  error        TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX ix_sims_proj_plat   ON sims(project_id, platform);
CREATE INDEX ix_sims_status      ON sims(status);

CREATE TABLE sim_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  sim_id        TEXT    NOT NULL REFERENCES sims(id) ON DELETE CASCADE,
  project_id    TEXT    NOT NULL,
  source_app    TEXT    NOT NULL,             -- 'code' | 'manual'
  source_ref    TEXT    NOT NULL DEFAULT '',  -- repo_id when source_app='code'
  framework     TEXT    NOT NULL DEFAULT '',  -- android | ios
  bundle_id     TEXT    NOT NULL DEFAULT '',
  artifact_path TEXT    NOT NULL DEFAULT '',  -- /data/artifacts/<sha>.{apk|app}
  status        TEXT    NOT NULL DEFAULT 'building',
                                              -- building | installing | running | stopped | crashed
  log_path      TEXT    NOT NULL DEFAULT '',  -- per-run build/install log file
  started_at    TIMESTAMP,
  stopped_at    TIMESTAMP,
  error         TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX ix_sim_runs_sim     ON sim_runs(sim_id, started_at DESC);
CREATE INDEX ix_sim_runs_status  ON sim_runs(status);

CREATE TABLE sim_streams (
  sim_id     TEXT    PRIMARY KEY REFERENCES sims(id) ON DELETE CASCADE,
  ws_token   TEXT    NOT NULL UNIQUE,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NOT NULL
);

CREATE INDEX ix_sim_streams_exp ON sim_streams(expires_at);
