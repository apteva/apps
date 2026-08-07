CREATE TABLE gig_public_domains (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  hostname        TEXT    NOT NULL,
  apex_domain     TEXT    NOT NULL,
  dns_name        TEXT    NOT NULL,
  dns_type        TEXT    NOT NULL,
  dns_value       TEXT    NOT NULL,
  dns_managed     INTEGER NOT NULL DEFAULT 0,
  ingress_target  TEXT    NOT NULL,
  is_default      INTEGER NOT NULL DEFAULT 0,
  status          TEXT    NOT NULL DEFAULT 'active',
  status_detail   TEXT,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, hostname)
);

CREATE UNIQUE INDEX ux_gig_public_domain_default
  ON gig_public_domains(project_id)
  WHERE is_default = 1;

CREATE INDEX ix_gig_public_domains_project
  ON gig_public_domains(project_id, status, hostname);

-- Snapshot the selected base URL so links already distributed to workers
-- do not change when the project's default public domain changes.
ALTER TABLE gig_assignments ADD COLUMN public_base_url TEXT;
