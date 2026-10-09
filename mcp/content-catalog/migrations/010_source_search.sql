-- Supports exact source and recursive descendant reads without changing links.
CREATE INDEX ix_asset_sources_parent ON asset_sources(project_id,source_asset_id,child_asset_id);
