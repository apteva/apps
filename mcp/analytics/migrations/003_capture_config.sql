-- Analytics v0.6 — auto-capture config.
--
-- Single-row config for the bus auto-capture subscriber (the _all
-- firehose consumer). Off by default: capturing every app's bus traffic
-- is a deliberate, volume + privacy-relevant choice, flipped on from the
-- panel's Capture tab. mode + topic_patterns shape what gets recorded;
-- sample_rate throttles high-traffic instances.

CREATE TABLE capture_config (
  id             INTEGER PRIMARY KEY CHECK (id = 1),

  -- Master switch. 0 = subscriber idles (no recording); 1 = records.
  enabled        INTEGER NOT NULL DEFAULT 0,

  -- 'all'       — record every captured topic.
  -- 'denylist'  — record everything EXCEPT topics matching topic_patterns.
  -- 'allowlist' — record ONLY topics matching topic_patterns.
  mode           TEXT    NOT NULL DEFAULT 'denylist',

  -- JSON array of glob-ish patterns: exact ("file.deleted"), prefix
  -- ("campaign.*"), or "*". Interpreted per mode.
  topic_patterns TEXT    NOT NULL DEFAULT '[]',

  -- 0..1 — fraction of matching events to keep (1 = all).
  sample_rate    REAL    NOT NULL DEFAULT 1.0,

  updated_at     INTEGER
);

INSERT INTO capture_config (id, enabled, mode, topic_patterns, sample_rate)
VALUES (1, 0, 'denylist', '[]', 1.0);
