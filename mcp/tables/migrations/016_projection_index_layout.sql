-- Preserve all existing indexes. Layout changes are explicit, transactional builds.
ALTER TABLE projection_indexes ADD COLUMN layout TEXT NOT NULL DEFAULT 'generation_first'
  CHECK (layout IN ('generation_first','filter_first'));
-- Empty names refer to the legacy deterministic index identity. Replacements
-- retain their distinct identity so restart, inheritance and drop use the right index.
ALTER TABLE projection_indexes ADD COLUMN physical_name TEXT NOT NULL DEFAULT '';
