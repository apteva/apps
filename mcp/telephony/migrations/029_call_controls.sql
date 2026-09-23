ALTER TABLE calls ADD COLUMN hold_state TEXT NOT NULL DEFAULT 'active';
ALTER TABLE calls ADD COLUMN recording_control_state TEXT NOT NULL DEFAULT 'default';
ALTER TABLE calls ADD COLUMN control_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN control_action TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN control_error TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN control_requested_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN hold_client_state TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN hold_control_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN hold_control_action TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN hold_requested_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN recording_control_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN recording_control_action TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN recording_requested_at TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS call_control_settings (
    project_id TEXT PRIMARY KEY,
    hold_music_url TEXT NOT NULL DEFAULT '',
    hold_music_storage_file_id INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);
