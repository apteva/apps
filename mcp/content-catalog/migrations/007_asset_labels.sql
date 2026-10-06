ALTER TABLE assets ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0 CHECK(favorite IN (0,1));
ALTER TABLE assets ADD COLUMN patreon_intent TEXT NOT NULL DEFAULT 'unset' CHECK(patreon_intent IN ('unset','free','paid'));
CREATE INDEX ix_assets_labels ON assets(project_id,favorite,patreon_intent);
CREATE TABLE asset_tags (
  project_id TEXT NOT NULL,
  asset_id TEXT NOT NULL REFERENCES assets(id),
  tag TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,asset_id,tag)
);
CREATE INDEX ix_asset_tags_lookup ON asset_tags(project_id,tag,asset_id);
