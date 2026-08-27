-- Preserve both sides of a render request. `params` remains exactly what the
-- caller submitted; `resolved_params` records executor-injected values such as
-- the concrete Smart Crop rectangle/path and source audio sample rate.
--
-- NULL means the render has not reached parameter resolution yet. Keeping the
-- column nullable also makes old render rows unambiguous after migration.
ALTER TABLE renders ADD COLUMN resolved_params TEXT
  CHECK (resolved_params IS NULL OR json_valid(resolved_params));
