-- Provider-neutral persistent-volume inventory. Boot and data are roles;
-- block/network/local/ephemeral are storage classes. They are intentionally
-- separate so, for example, a Scaleway SBS volume can be both boot and block.

ALTER TABLE instances ADD COLUMN provider_connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN storage_json TEXT NOT NULL DEFAULT '{}';

DROP INDEX ux_instances_provider_id;
CREATE UNIQUE INDEX ux_instances_connection_provider_id
  ON instances(provider_connection_id, provider, provider_id)
  WHERE provider_id <> '';

CREATE TABLE instance_volumes (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_id             INTEGER,
  provider                TEXT    NOT NULL,
  provider_connection_id  INTEGER NOT NULL DEFAULT 0,
  provider_volume_id      TEXT    NOT NULL,
  name                    TEXT    NOT NULL DEFAULT '',
  role                    TEXT    NOT NULL DEFAULT 'data',
  storage_class           TEXT    NOT NULL DEFAULT 'block',
  tier                    TEXT    NOT NULL DEFAULT 'provider-default',
  provider_type           TEXT    NOT NULL DEFAULT '',
  size_gb                 INTEGER NOT NULL DEFAULT 0,
  region                  TEXT    NOT NULL DEFAULT '',
  status                  TEXT    NOT NULL DEFAULT 'creating',
  filesystem              TEXT    NOT NULL DEFAULT '',
  mount_path              TEXT    NOT NULL DEFAULT '',
  device_path             TEXT    NOT NULL DEFAULT '',
  managed                 INTEGER NOT NULL DEFAULT 1,
  delete_policy           TEXT    NOT NULL DEFAULT 'retain',
  provider_metadata_json  TEXT    NOT NULL DEFAULT '{}',
  error_message           TEXT    NOT NULL DEFAULT '',
  created_at              DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at              DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(instance_id) REFERENCES instances(id) ON DELETE SET NULL,
  UNIQUE(provider_connection_id, provider, provider_volume_id),
  CHECK(role IN ('boot','data')),
  CHECK(storage_class IN ('local','block','network','ephemeral')),
  CHECK(delete_policy IN ('retain','with_instance'))
);

CREATE INDEX ix_instance_volumes_instance ON instance_volumes(instance_id);
CREATE INDEX ix_instance_volumes_provider ON instance_volumes(provider, provider_connection_id);
