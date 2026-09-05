-- Never reuse a public host handle, even after deleting its inventory row.
CREATE TABLE instance_ids (id INTEGER PRIMARY KEY AUTOINCREMENT);
INSERT INTO instance_ids(id) SELECT id FROM instances WHERE id > 0;
CREATE TRIGGER reserve_imported_instance_id AFTER INSERT ON instances WHEN NEW.id > 0
BEGIN INSERT OR IGNORE INTO instance_ids(id) VALUES (NEW.id); END;

ALTER TABLE instances ADD COLUMN create_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN destroy_options_json TEXT NOT NULL DEFAULT '{}';

-- Persist mutation intent separately from a resource's observed provider state.
CREATE TABLE resource_operations (
 resource_kind TEXT NOT NULL,
 resource_id INTEGER NOT NULL,
 token TEXT NOT NULL,
 operation TEXT NOT NULL,
 input_json TEXT NOT NULL DEFAULT '{}',
 error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(resource_kind,resource_id)
);
CREATE TABLE object_storage_key_cleanup (
 object_storage_id INTEGER NOT NULL,
 connection_id INTEGER NOT NULL,
 access_key_id TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(connection_id,access_key_id)
);
ALTER TABLE instance_volumes ADD COLUMN provider_device_path TEXT NOT NULL DEFAULT '';
UPDATE instance_volumes SET provider_device_path=device_path WHERE provider='aws-ec2' AND device_path LIKE '/dev/sd%';
