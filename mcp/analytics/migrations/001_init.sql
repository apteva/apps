-- Analytics v0.1 — single wide events table.
--
-- Generic by design: app + topic + JSON props. No per-topic schema,
-- no per-event row structure. Indexed for the three common access
-- patterns (recent activity, per (app, topic) trend, per project).

CREATE TABLE events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,

  -- Unix epoch milliseconds. Caller may override (back-dating an
  -- imported event); default is time.Now().UnixMilli() at the handler.
  ts          INTEGER NOT NULL,

  -- The app that owns the event ('social', 'storage', 'crm', …).
  -- For explicit analytics_track calls without an app field,
  -- defaults to the literal string '_explicit' so dashboards can
  -- distinguish "logged via track but caller didn't say who".
  app         TEXT    NOT NULL,

  -- App-relative event name. Convention: dotted ('post.created',
  -- 'target.published'). Free-form.
  topic       TEXT    NOT NULL,

  -- Project scope when known. Nullable — global-scope events
  -- (instance-wide, not tied to one project) leave it null.
  project_id  TEXT,

  -- Optional caller install id. Useful for "which install of social
  -- emitted this" when an instance has multiple installs of the
  -- same app.
  install_id  INTEGER,

  -- Optional user / session attribution. App decides whether to
  -- populate these — analytics doesn't enforce a session model.
  user_id     TEXT,
  session_id  TEXT,

  -- 'auto' (came in via the firehose subscriber, v0.2+) or
  -- 'track' (explicit analytics_track call). Lets queries
  -- distinguish synthetic-from-emit traffic vs deliberate logging.
  source      TEXT NOT NULL,

  -- Free-form JSON object. Empty object when caller passed nothing.
  -- Queried via SQLite's json_extract — no column-promotion in v0.1.
  props       TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX ix_events_ts            ON events(ts);
CREATE INDEX ix_events_app_topic_ts  ON events(app, topic, ts);
CREATE INDEX ix_events_project_ts    ON events(project_id, ts);
