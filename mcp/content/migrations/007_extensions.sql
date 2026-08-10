-- Generic app-provided public surfaces.
--
-- Content owns routing, rendering, themes, previews, and browser actions.
-- Provider apps install opaque declarative manifests; Content never needs
-- provider-specific columns or code.

CREATE TABLE content_extensions (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id         TEXT    NOT NULL,
  site_id            INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  extension_key      TEXT    NOT NULL,
  provider_app       TEXT    NOT NULL,
  display_name       TEXT    NOT NULL DEFAULT '',
  version            TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT 'draft',
  draft_manifest     TEXT    NOT NULL DEFAULT '{}',
  published_manifest TEXT    NOT NULL DEFAULT '{}',
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  published_at       TIMESTAMP,
  UNIQUE(project_id, site_id, extension_key)
);

CREATE INDEX content_extensions_site_status_idx
  ON content_extensions(project_id, site_id, status, updated_at DESC);

CREATE TABLE content_extension_versions (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  extension_id   INTEGER NOT NULL REFERENCES content_extensions(id) ON DELETE CASCADE,
  version        TEXT    NOT NULL DEFAULT '',
  manifest       TEXT    NOT NULL,
  published_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX content_extension_versions_extension_idx
  ON content_extension_versions(extension_id, published_at DESC);
