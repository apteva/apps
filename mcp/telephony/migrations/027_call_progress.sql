ALTER TABLE calls ADD COLUMN answered_by TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN termination_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN machine_detection TEXT NOT NULL DEFAULT 'off';
ALTER TABLE calls ADD COLUMN machine_detection_action TEXT NOT NULL DEFAULT 'notify';

CREATE TABLE IF NOT EXISTS outbound_settings (
    project_id               TEXT PRIMARY KEY,
    machine_detection        TEXT NOT NULL DEFAULT 'off',
    machine_detection_action TEXT NOT NULL DEFAULT 'notify',
    updated_at               TEXT NOT NULL
);
