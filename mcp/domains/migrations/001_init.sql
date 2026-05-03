-- Domains v0.1.
--
-- Local inventory of domains the project has registered with this
-- app. The actual domain registration + DNS data live at the
-- registrar/DNS provider — this table just tracks "which domains
-- does this project use messaging/storage/etc. for, and via which
-- connection." Records are always fetched live from the provider;
-- no local mirror.

CREATE TABLE domains (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  name              TEXT    NOT NULL,            -- e.g. "acme.com" — apex only, no scheme/path
  registrar_slug    TEXT,                        -- "porkbun" | "namecheap" | "" when unknown
  dns_provider_slug TEXT,                        -- usually same as registrar; can differ
  expires_at        TIMESTAMP,                   -- domain registration expiry, refreshed on sync
  notes             TEXT NOT NULL DEFAULT '',
  metadata          TEXT NOT NULL DEFAULT '{}',  -- arbitrary JSON, app-specific
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at        TIMESTAMP                    -- soft delete
);

CREATE UNIQUE INDEX ix_domains_proj_name
  ON domains(project_id, name)
  WHERE deleted_at IS NULL;
CREATE INDEX ix_domains_proj_status
  ON domains(project_id, deleted_at);
