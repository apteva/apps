-- Generic webinar slots. A webinar is the funnel/content definition;
-- a slot is one concrete time a registrant can attend.

ALTER TABLE webinars ADD COLUMN scheduling_mode TEXT NOT NULL DEFAULT 'single';
ALTER TABLE webinars ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE webinars ADD COLUMN slot_duration_minutes INTEGER;
ALTER TABLE webinars ADD COLUMN registration_policy TEXT;

CREATE TABLE webinar_slots (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT NOT NULL,
  webinar_id     INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  starts_at      TIMESTAMP NOT NULL,
  ends_at        TIMESTAMP,
  timezone       TEXT,
  capacity       INTEGER,
  status         TEXT NOT NULL DEFAULT 'scheduled',
  source         TEXT NOT NULL DEFAULT 'manual',
  generation_key TEXT,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP
);

CREATE INDEX ix_webinar_slots_lookup
  ON webinar_slots(project_id, webinar_id, starts_at);

CREATE INDEX ix_webinar_slots_available
  ON webinar_slots(project_id, webinar_id, status, starts_at);

CREATE UNIQUE INDEX ux_webinar_slots_generation
  ON webinar_slots(project_id, webinar_id, generation_key)
  WHERE generation_key IS NOT NULL;

ALTER TABLE webinar_registrants ADD COLUMN slot_id INTEGER REFERENCES webinar_slots(id);
CREATE INDEX ix_webinar_registrants_slot ON webinar_registrants(slot_id);

INSERT INTO webinar_slots
  (project_id, webinar_id, starts_at, ends_at, timezone, status, source)
SELECT
  project_id,
  id,
  scheduled_at,
  datetime(scheduled_at, '+' || duration_minutes || ' minutes'),
  timezone,
  'scheduled',
  'scheduled_at'
FROM webinars
WHERE scheduled_at IS NOT NULL AND scheduled_at <> '';
