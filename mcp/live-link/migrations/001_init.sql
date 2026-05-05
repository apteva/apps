-- Live Link app v0.1 — one row per tunnel run.
--
-- A "run" is a single lifecycle of the cloudflared subprocess: from
-- the moment we spawned it to the moment it exited (clean stop, crash,
-- or sidecar restart). The run currently in flight has finished_at IS
-- NULL and status='running'; everything else is terminal.
--
-- On sidecar boot we mark any leftover running rows as 'orphaned' —
-- the previous process is gone, so the tunnel is too.

CREATE TABLE runs (
  id              INTEGER PRIMARY KEY,
  provider        TEXT    NOT NULL,                       -- 'cloudflared' (only option in v0.1)
  target_url      TEXT    NOT NULL,                       -- the local URL we forwarded to
  public_url      TEXT    NOT NULL DEFAULT '',            -- assigned trycloudflare.com URL; populated once parsed from cloudflared's stderr
  pid             INTEGER NOT NULL DEFAULT 0,             -- subprocess pid (process-local; not durable across sidecar restarts)
  started_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at     TIMESTAMP,
  status          TEXT    NOT NULL DEFAULT 'running',     -- 'running' | 'stopped' | 'failed' | 'orphaned'
  exit_reason     TEXT    NOT NULL DEFAULT ''             -- short human-readable note: "user stopped", "binary not found", "exit 1", …
);
CREATE INDEX ix_runs_status ON runs(status, started_at DESC);
