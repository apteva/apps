-- Live Link v0.5 — durable operator intent and bounded history.
--
-- Process state (a child happens to be running) is deliberately separate
-- from operator intent (the tunnel should be live). Clean sidecar shutdowns
-- stop the child but preserve desired_live, so boot reconciliation can bring
-- every provider back consistently.

CREATE TABLE runtime_state (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  active_provider TEXT    NOT NULL,
  desired_live    INTEGER NOT NULL DEFAULT 0 CHECK (desired_live IN (0, 1)),
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The old schema persisted Cloudflare connector tokens. v0.5 fetches them
-- just-in-time through the integration proxy, so erase legacy copies.
UPDATE named_tunnels SET tunnel_token = '';

-- History reads are ordered by recency without filtering by status; the old
-- (status, started_at) index could not serve that query.
CREATE INDEX ix_runs_started_at ON runs(started_at DESC, id DESC);
