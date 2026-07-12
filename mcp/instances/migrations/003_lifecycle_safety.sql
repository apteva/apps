-- Allow multiple rows that have not received an upstream id yet, while
-- retaining uniqueness once a provider id is known. The rebuild also adds
-- durable upgrade intent and TOFU SSH host-key pinning.

ALTER TABLE instances RENAME TO instances_old;

CREATE TABLE instances (
  id                 INTEGER PRIMARY KEY,
  name               TEXT    NOT NULL,
  provider           TEXT    NOT NULL,
  provider_id        TEXT    NOT NULL DEFAULT '',
  public_ipv4        TEXT    NOT NULL DEFAULT '',
  public_ipv6        TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT 'pending',
  region             TEXT    NOT NULL DEFAULT '',
  size               TEXT    NOT NULL DEFAULT '',
  image              TEXT    NOT NULL DEFAULT '',
  ssh_user           TEXT    NOT NULL DEFAULT '',
  ssh_host           TEXT    NOT NULL DEFAULT '',
  ssh_port           INTEGER NOT NULL DEFAULT 22,
  ssh_private_key    TEXT    NOT NULL DEFAULT '',
  ssh_public_key     TEXT    NOT NULL DEFAULT '',
  ssh_host_key       TEXT    NOT NULL DEFAULT '',
  tags_json          TEXT    NOT NULL DEFAULT '[]',
  resources_json     TEXT    NOT NULL DEFAULT '{}',
  ports_json         TEXT    NOT NULL DEFAULT '{}',
  monthly_cost_cents INTEGER NOT NULL DEFAULT 0,
  pending_size       TEXT    NOT NULL DEFAULT '',
  error_message      TEXT    NOT NULL DEFAULT '',
  created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
  ready_at           DATETIME,
  destroyed_at       DATETIME
);

INSERT INTO instances (
  id, name, provider, provider_id, public_ipv4, public_ipv6, status,
  region, size, image, ssh_user, ssh_host, ssh_port, ssh_private_key,
  ssh_public_key, tags_json, resources_json, ports_json,
  monthly_cost_cents, error_message, created_at, ready_at, destroyed_at
)
SELECT
  id, name, provider, provider_id, public_ipv4, public_ipv6, status,
  region, size, image, ssh_user, ssh_host, ssh_port, ssh_private_key,
  ssh_public_key, tags_json, resources_json, ports_json,
  monthly_cost_cents, error_message, created_at, ready_at, destroyed_at
FROM instances_old;

DROP TABLE instances_old;

CREATE UNIQUE INDEX ux_instances_provider_id
  ON instances(provider, provider_id)
  WHERE provider_id <> '';
CREATE INDEX ix_instances_status ON instances(status);
CREATE INDEX ix_instances_provider ON instances(provider);
