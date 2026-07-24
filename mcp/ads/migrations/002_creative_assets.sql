-- Track assets uploaded through this app so status checks remain scoped to
-- the project and ad account that created them.

CREATE TABLE IF NOT EXISTS creative_assets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    platform TEXT NOT NULL,
    native_asset_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_creative_assets_native
    ON creative_assets(project_id, ad_account_id, platform, native_asset_id);
