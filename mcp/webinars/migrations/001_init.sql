-- Webinars v0.1 — funnel + live + replay.
--
-- Every table is partitioned by project_id so the same schema serves
-- both `scope: project` (one install per project) and `scope: global`
-- (one install across projects, project_id is the isolation boundary).
--
-- Streaming is the source of truth for the pipe (RTMP→HLS, recording,
-- aggregate viewer counts). Webinars layers identity on top: who
-- registered, who actually watched, what they engaged with.

CREATE TABLE webinars (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  slug                TEXT    NOT NULL,
  title               TEXT    NOT NULL,
  host_name           TEXT,
  description         TEXT,
  kind                TEXT    NOT NULL DEFAULT 'scheduled',
                                                    -- live | scheduled | replay
  scheduled_at        TIMESTAMP,
  duration_minutes    INTEGER NOT NULL DEFAULT 60,

  status              TEXT    NOT NULL DEFAULT 'draft',
                                                    -- draft|scheduled|live|ended|cancelled
  stream_id           INTEGER,                      -- streaming.streams.id (cross-app FK; not enforced)

  -- Replay state.
  recording_published INTEGER NOT NULL DEFAULT 0,
  replay_token        TEXT,                         -- gate for /replay/<slug>?t=<token>
  replay_expires_at   TIMESTAMP,

  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at          TIMESTAMP,
  ended_at            TIMESTAMP
);
CREATE UNIQUE INDEX ux_webinar_slug    ON webinars(project_id, slug);
CREATE INDEX ix_webinar_proj_status    ON webinars(project_id, status);
CREATE INDEX ix_webinar_proj_scheduled ON webinars(project_id, scheduled_at);
CREATE INDEX ix_webinar_stream         ON webinars(stream_id) WHERE stream_id IS NOT NULL;

-- One row per registration. join_token is the stable per-person
-- identifier embedded in /live/<token> URLs.
CREATE TABLE webinar_registrants (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  webinar_id      INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  contact_id      INTEGER,                          -- crm.contacts.id when CRM bound
  email           TEXT,
  phone           TEXT,
  display_name    TEXT,
  join_token      TEXT NOT NULL,
  registered_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  source          TEXT,                             -- "form" | "agent" | "import"
  attended_live   INTEGER NOT NULL DEFAULT 0,
  attended_replay INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX ux_reg_token        ON webinar_registrants(join_token);
CREATE INDEX        ix_reg_webinar      ON webinar_registrants(webinar_id, registered_at DESC);
-- Partial unique index: prevent the same email registering twice for
-- one webinar. NULLable email is fine — partial index lets nulls
-- through.
CREATE UNIQUE INDEX ux_reg_email        ON webinar_registrants(webinar_id, email)
  WHERE email IS NOT NULL AND email <> '';

-- Per-registrant attendance with watch-time accumulation. One row per
-- (registrant, source) — same registrant can have one row for live
-- and one for replay.
CREATE TABLE webinar_attendance (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  webinar_id      INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  registrant_id   INTEGER NOT NULL REFERENCES webinar_registrants(id) ON DELETE CASCADE,
  source          TEXT NOT NULL,                    -- live | replay
  joined_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  last_heartbeat  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  left_at         TIMESTAMP,
  watch_seconds   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX ux_attendance       ON webinar_attendance(registrant_id, source);
CREATE INDEX ix_attendance_active       ON webinar_attendance(webinar_id, last_heartbeat)
  WHERE left_at IS NULL;

-- Scripted offers (offset_seconds set at define-time) and ad-hoc
-- offers (offset_seconds NULL, posted live). shown_at is set when
-- the offer-broadcaster fires.
CREATE TABLE webinar_offers (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT NOT NULL,
  webinar_id       INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  offset_seconds   INTEGER,                         -- NULL = ad-hoc
  headline         TEXT NOT NULL,
  body             TEXT,
  cta_label        TEXT NOT NULL,
  cta_url          TEXT NOT NULL,
  duration_seconds INTEGER NOT NULL DEFAULT 30,
  shown_at         TIMESTAMP,
  sequence         INTEGER NOT NULL DEFAULT 0       -- monotonic per webinar (for live-room polling)
);
CREATE INDEX ix_offer_timeline ON webinar_offers(webinar_id, offset_seconds);
CREATE INDEX ix_offer_shown    ON webinar_offers(webinar_id, sequence DESC);

-- Click attribution.
CREATE TABLE webinar_offer_clicks (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  offer_id        INTEGER NOT NULL REFERENCES webinar_offers(id) ON DELETE CASCADE,
  registrant_id   INTEGER REFERENCES webinar_registrants(id) ON DELETE CASCADE,
  clicked_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_offer_click ON webinar_offer_clicks(offer_id, clicked_at DESC);

CREATE TABLE webinar_polls (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT NOT NULL,
  webinar_id       INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  question         TEXT NOT NULL,
  choices          TEXT NOT NULL,                   -- JSON array
  duration_seconds INTEGER NOT NULL DEFAULT 60,
  opened_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  closes_at        TIMESTAMP,
  sequence         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE webinar_poll_responses (
  poll_id         INTEGER NOT NULL REFERENCES webinar_polls(id) ON DELETE CASCADE,
  registrant_id   INTEGER NOT NULL REFERENCES webinar_registrants(id) ON DELETE CASCADE,
  choice_index    INTEGER NOT NULL,
  answered_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (poll_id, registrant_id)
);

CREATE TABLE webinar_chat (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  webinar_id      INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  registrant_id   INTEGER REFERENCES webinar_registrants(id) ON DELETE SET NULL,
  display_name    TEXT NOT NULL,                    -- snapshot at send-time
  body            TEXT NOT NULL,
  kind            TEXT NOT NULL DEFAULT 'message',  -- message | question | host
  sequence        INTEGER NOT NULL DEFAULT 0,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_chat_timeline ON webinar_chat(webinar_id, sequence DESC);

-- Reminder schedule + audit. The scheduler writes pending rows at
-- registration time; the cron worker dispatches them via messaging.
CREATE TABLE webinar_reminders (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  webinar_id      INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  registrant_id   INTEGER NOT NULL REFERENCES webinar_registrants(id) ON DELETE CASCADE,
  channel         TEXT NOT NULL,                    -- email | sms
  lead_label      TEXT NOT NULL,                    -- "T-24h" | "T-1h" | "T-15m" | "live"
  scheduled_for   TIMESTAMP NOT NULL,
  sent_at         TIMESTAMP,
  messaging_id    INTEGER,                          -- messaging.messages.id when sent
  status          TEXT NOT NULL DEFAULT 'pending',  -- pending|sent|skipped|failed
  error           TEXT
);
CREATE INDEX ix_reminder_due ON webinar_reminders(status, scheduled_for) WHERE status='pending';
