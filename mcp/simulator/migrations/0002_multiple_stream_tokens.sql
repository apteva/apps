-- A device can be viewed from Code and Simulator at the same time. Keep each
-- panel's short-lived bearer independently instead of rotating a single row.

CREATE TABLE sim_streams_multi (
  ws_token   TEXT    PRIMARY KEY,
  sim_id     TEXT    NOT NULL REFERENCES sims(id) ON DELETE CASCADE,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NOT NULL
);

INSERT INTO sim_streams_multi (ws_token, sim_id, created_at, expires_at)
SELECT ws_token, sim_id, created_at, expires_at FROM sim_streams;

DROP TABLE sim_streams;
ALTER TABLE sim_streams_multi RENAME TO sim_streams;

CREATE INDEX ix_sim_streams_sim ON sim_streams(sim_id);
CREATE INDEX ix_sim_streams_exp ON sim_streams(expires_at);
