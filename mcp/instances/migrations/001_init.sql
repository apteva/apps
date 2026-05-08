-- Apteva Instances v0.1.0 — compute-host inventory.
--
-- One row per machine that workloads can run on. The local Apteva
-- machine is modelled as a built-in instance (id 0) seeded at app
-- mount time. Remote instances come from VPS providers via the
-- bound integration.
--
-- Concept boundary: "instance" here means a machine you can SSH
-- into. Distinct from apteva-core's existing "instance" concept
-- (a thinking-loop running for a project) — that's an internal-
-- to-apteva-server model, not exposed at the apps layer. They
-- share a word; they don't share data or code.

CREATE TABLE instances (
  id                 INTEGER PRIMARY KEY,                 -- explicit; 0 reserved for local
  name               TEXT    NOT NULL,
  provider           TEXT    NOT NULL,                    -- 'local' | 'hetzner' | future
  provider_id        TEXT    NOT NULL DEFAULT '',         -- upstream resource id; '' for local
  public_ipv4        TEXT    NOT NULL DEFAULT '',
  public_ipv6        TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT 'pending',  -- pending|provisioning|ready|error|destroyed
  region             TEXT    NOT NULL DEFAULT '',
  size               TEXT    NOT NULL DEFAULT '',
  image              TEXT    NOT NULL DEFAULT '',
  ssh_user           TEXT    NOT NULL DEFAULT '',
  ssh_private_key    TEXT    NOT NULL DEFAULT '',         -- encrypted at rest in v0.2; plain in v0.1
  ssh_public_key     TEXT    NOT NULL DEFAULT '',
  tags_json          TEXT    NOT NULL DEFAULT '[]',
  monthly_cost_cents INTEGER NOT NULL DEFAULT 0,
  error_message      TEXT    NOT NULL DEFAULT '',
  created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
  ready_at           DATETIME,
  destroyed_at       DATETIME,

  UNIQUE(provider, provider_id)
);

CREATE INDEX ix_instances_status ON instances(status);
CREATE INDEX ix_instances_provider ON instances(provider);
