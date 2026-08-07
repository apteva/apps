-- Generalize the creative asset ownership ledger into a provider-neutral
-- resource registry. Upstream providers remain the source of truth; these
-- rows provide stable, project-scoped references and remembered defaults.

ALTER TABLE creative_assets RENAME TO ad_resources;
ALTER TABLE ad_resources RENAME COLUMN kind TO provider_type;

ALTER TABLE ad_resources ADD COLUMN kind TEXT NOT NULL DEFAULT 'creative_asset';
ALTER TABLE ad_resources ADD COLUMN parent_resource_id INTEGER;
ALTER TABLE ad_resources ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE ad_resources ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE ad_resources ADD COLUMN capabilities_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE ad_resources ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE ad_resources ADD COLUMN managed_by_app INTEGER NOT NULL DEFAULT 1;
ALTER TABLE ad_resources ADD COLUMN refreshed_at TIMESTAMP;

DROP INDEX IF EXISTS idx_creative_assets_native;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ad_resources_native
    ON ad_resources(project_id, ad_account_id, kind, provider_type, native_asset_id);
CREATE INDEX IF NOT EXISTS idx_ad_resources_lookup
    ON ad_resources(project_id, ad_account_id, kind, status);
CREATE INDEX IF NOT EXISTS idx_ad_resources_parent
    ON ad_resources(parent_resource_id);

CREATE TABLE IF NOT EXISTS ad_resource_defaults (
    project_id TEXT NOT NULL,
    ad_account_id INTEGER NOT NULL,
    purpose TEXT NOT NULL,
    resource_id INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(project_id, ad_account_id, purpose)
);

CREATE INDEX IF NOT EXISTS idx_ad_resource_defaults_resource
    ON ad_resource_defaults(resource_id);
