-- Keep the original upload folder stable if a historical recording date is
-- filled in later. An empty session_date means unknown, not today's date.
ALTER TABLE sessions ADD COLUMN storage_folder TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN host_collection_id TEXT NOT NULL DEFAULT '';
UPDATE sessions SET storage_folder =
  rtrim((SELECT storage_root FROM brands b WHERE b.project_id=sessions.project_id AND b.id=sessions.brand_id), '/') ||
  '/sessions/' || CASE WHEN session_date='' THEN 'undated' ELSE session_date END || '/' || id || '/';

-- source_sha256 is the Catalog Storage asset snapshot. It does not prove that
-- a pre-existing Bunny video contains the same bytes.
ALTER TABLE hostings ADD COLUMN link_origin TEXT NOT NULL DEFAULT 'catalog_upload';
ALTER TABLE hostings ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hostings ADD COLUMN provider_verified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE hostings ADD COLUMN source_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE hostings ADD COLUMN provider_source_match TEXT NOT NULL DEFAULT 'catalog_transfer_requested';
