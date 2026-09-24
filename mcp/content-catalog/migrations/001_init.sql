-- Catalog IDs are stable UUIDs. External references include app install IDs;
-- no other app's database is copied or modified by this schema.
CREATE TABLE brands (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  storage_root TEXT NOT NULL,
  host_provider TEXT NOT NULL DEFAULT '',
  host_connection_id INTEGER NOT NULL DEFAULT 0,
  host_library_id TEXT NOT NULL DEFAULT '',
  host_collection_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, slug)
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  brand_id TEXT NOT NULL REFERENCES brands(id),
  title TEXT NOT NULL,
  session_date TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','active','completed','archived')),
  notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_sessions_brand ON sessions(project_id, brand_id, session_date DESC);

CREATE TABLE session_gigs (
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  gigs_install_id INTEGER NOT NULL,
  gig_id INTEGER NOT NULL,
  role TEXT NOT NULL DEFAULT '',
  linked_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, session_id, gigs_install_id, gig_id)
);

CREATE TABLE assets (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  storage_install_id INTEGER NOT NULL,
  storage_file_id TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'other',
  content_type TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  review_status TEXT NOT NULL DEFAULT 'pending' CHECK(review_status IN ('pending','approved','rejected')),
  media_status TEXT NOT NULL DEFAULT 'unknown',
  media_rating TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, session_id, storage_install_id, storage_file_id)
);
CREATE INDEX ix_assets_session ON assets(project_id, session_id, created_at DESC);

CREATE TABLE asset_sources (
  project_id TEXT NOT NULL,
  child_asset_id TEXT NOT NULL REFERENCES assets(id),
  source_asset_id TEXT NOT NULL REFERENCES assets(id),
  relation TEXT NOT NULL DEFAULT 'derived',
  source_order INTEGER NOT NULL DEFAULT 0,
  media_render_id INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id, child_asset_id, source_asset_id),
  CHECK(child_asset_id != source_asset_id)
);

CREATE TABLE releases (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  brand_id TEXT NOT NULL REFERENCES brands(id),
  title TEXT NOT NULL,
  phase TEXT NOT NULL DEFAULT '',
  audience TEXT NOT NULL DEFAULT '',
  planned_at TEXT NOT NULL DEFAULT '',
  approval_status TEXT NOT NULL DEFAULT 'draft' CHECK(approval_status IN ('draft','approved','cancelled')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_releases_brand ON releases(project_id, brand_id, planned_at);

CREATE TABLE release_targets (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  release_id TEXT NOT NULL REFERENCES releases(id),
  destination TEXT NOT NULL,
  account_ref TEXT NOT NULL DEFAULT '',
  planned_at TEXT NOT NULL DEFAULT '',
  current_status TEXT NOT NULL DEFAULT 'planned',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, release_id, destination, account_ref)
);

CREATE TABLE release_target_assets (
  project_id TEXT NOT NULL,
  target_id TEXT NOT NULL REFERENCES release_targets(id),
  asset_id TEXT NOT NULL REFERENCES assets(id),
  position INTEGER NOT NULL,
  PRIMARY KEY(project_id, target_id, asset_id),
  UNIQUE(project_id, target_id, position)
);

CREATE TABLE publication_observations (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  target_id TEXT NOT NULL REFERENCES release_targets(id),
  status TEXT NOT NULL CHECK(status IN ('scheduled','submitted','provider_reported_published','verified_published','failed','removed','unknown')),
  external_post_id TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  actual_at TEXT NOT NULL DEFAULT '',
  evidence_source TEXT NOT NULL,
  failure_details TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_publications_target ON publication_observations(project_id, target_id, observed_at DESC);

CREATE TABLE hostings (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  asset_id TEXT NOT NULL REFERENCES assets(id),
  provider TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  library_id TEXT NOT NULL,
  collection_id TEXT NOT NULL DEFAULT '',
  source_sha256 TEXT NOT NULL,
  remote_id TEXT NOT NULL DEFAULT '',
  embed_url TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN ('reserved','uncertain','processing','ready','failed')),
  error TEXT NOT NULL DEFAULT '',
  last_checked_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, asset_id, provider, connection_id, library_id, collection_id, source_sha256)
);
CREATE INDEX ix_hostings_asset ON hostings(project_id, asset_id, created_at DESC);
