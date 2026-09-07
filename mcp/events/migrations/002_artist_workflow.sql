CREATE TABLE event_settings (
 event_id INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
 settings_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE application_identities (
 event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
 identity TEXT NOT NULL,
 application_id INTEGER NOT NULL REFERENCES performer_applications(id) ON DELETE CASCADE,
 PRIMARY KEY(event_id, identity)
);
CREATE TABLE application_photos (
 application_id INTEGER PRIMARY KEY REFERENCES performer_applications(id) ON DELETE CASCADE,
 content_type TEXT NOT NULL,
 data BLOB NOT NULL,
 consent INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_events_public_upcoming ON events(project_id, visibility, status, starts_at);
