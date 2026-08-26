-- bookings v0.2: distinguish conflict/destination calendars and make retries idempotent.

ALTER TABLE booking_types ADD COLUMN destination_calendar_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS ux_bookings_project_idempotency
  ON bookings(project_id, idempotency_key)
  WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS ix_bookings_project_type_status_time
  ON bookings(project_id, booking_type_id, status, start_at, end_at);
