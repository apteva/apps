-- Bookings v0.3: efficient upcoming Calls-booking reads for Home widgets.

CREATE INDEX IF NOT EXISTS ix_bookings_project_active_calls_time
  ON bookings(project_id, start_at, id)
  WHERE calls_room_id IS NOT NULL
    AND calls_room_id > 0
    AND status IN ('confirmed', 'rescheduled');
