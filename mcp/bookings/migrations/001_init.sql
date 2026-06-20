-- bookings v0.1: booking types, confirmed bookings, and lifecycle audit.

CREATE TABLE IF NOT EXISTS booking_types (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  duration_minutes INTEGER NOT NULL DEFAULT 30,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  location_kind TEXT NOT NULL DEFAULT 'calls'
    CHECK(location_kind IN ('calls','phone','in_person','external_url')),
  location_value TEXT NOT NULL DEFAULT '',
  target_kind TEXT NOT NULL DEFAULT 'human'
    CHECK(target_kind IN ('human','ai_agent','either','team')),
  calendar_ids TEXT NOT NULL DEFAULT '[]',
  agent_instance_id TEXT NOT NULL DEFAULT '',
  calls_enabled INTEGER NOT NULL DEFAULT 1,
  crm_enabled INTEGER NOT NULL DEFAULT 1,
  active INTEGER NOT NULL DEFAULT 1,
  availability_rules TEXT NOT NULL DEFAULT '{}',
  intake_schema TEXT NOT NULL DEFAULT '[]',
  confirmation_policy TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_booking_types_project_slug
  ON booking_types(project_id, slug);
CREATE INDEX IF NOT EXISTS ix_booking_types_project_active
  ON booking_types(project_id, active);

CREATE TABLE IF NOT EXISTS bookings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  booking_type_id INTEGER NOT NULL REFERENCES booking_types(id),
  status TEXT NOT NULL DEFAULT 'confirmed'
    CHECK(status IN ('confirmed','cancelled','rescheduled','completed','no_show')),
  start_at TEXT NOT NULL,
  end_at TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  invitee_name TEXT NOT NULL DEFAULT '',
  invitee_email TEXT NOT NULL DEFAULT '',
  invitee_phone TEXT NOT NULL DEFAULT '',
  intake_answers TEXT NOT NULL DEFAULT '{}',
  calendar_event_id INTEGER,
  calls_room_id INTEGER,
  calls_guest_join_url TEXT NOT NULL DEFAULT '',
  calls_host_join_url TEXT NOT NULL DEFAULT '',
  crm_contact_id INTEGER,
  assigned_target_kind TEXT NOT NULL DEFAULT '',
  assigned_agent_instance_id TEXT NOT NULL DEFAULT '',
  cancellation_token TEXT NOT NULL UNIQUE,
  reschedule_token TEXT NOT NULL UNIQUE,
  source TEXT NOT NULL DEFAULT 'manual',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_bookings_project_time
  ON bookings(project_id, start_at);
CREATE INDEX IF NOT EXISTS ix_bookings_project_status
  ON bookings(project_id, status);
CREATE INDEX IF NOT EXISTS ix_bookings_type
  ON bookings(booking_type_id, start_at);

CREATE TABLE IF NOT EXISTS booking_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  booking_id INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_booking_events_booking
  ON booking_events(booking_id, id);
