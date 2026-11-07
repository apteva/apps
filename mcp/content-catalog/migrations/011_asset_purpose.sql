-- Purpose is explicit editorial metadata, separate from ancestry and lifecycle.
-- Existing records stay unclassified; do not infer roles from names or sources.
ALTER TABLE assets ADD COLUMN role TEXT NOT NULL DEFAULT 'unspecified' CHECK(role IN ('unspecified','main','derivative','intermediate'));
ALTER TABLE assets ADD COLUMN output_type TEXT NOT NULL DEFAULT '';
CREATE INDEX ix_assets_role ON assets(project_id,role,lifecycle);
CREATE TABLE asset_purpose_events (
 id TEXT PRIMARY KEY,project_id TEXT NOT NULL,asset_id TEXT NOT NULL REFERENCES assets(id),
 previous_role TEXT NOT NULL,role TEXT NOT NULL,previous_output_type TEXT NOT NULL,output_type TEXT NOT NULL,created_at TEXT NOT NULL
);
CREATE INDEX ix_asset_purpose_events ON asset_purpose_events(project_id,asset_id,created_at);
