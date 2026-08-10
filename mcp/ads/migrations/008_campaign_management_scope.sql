-- Project-level campaign management. Existing accounts retain the historical
-- account-wide behavior; newly finalized accounts explicitly opt into selected
-- mode in application code.
ALTER TABLE ad_accounts
    ADD COLUMN management_mode TEXT NOT NULL DEFAULT 'all'
    CHECK (management_mode IN ('all', 'selected'));

ALTER TABLE ad_entities ADD COLUMN is_managed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ad_entities ADD COLUMN managed_source TEXT NOT NULL DEFAULT '';
ALTER TABLE ad_entities ADD COLUMN managed_at TEXT;

CREATE INDEX IF NOT EXISTS idx_ad_entities_managed_campaign
    ON ad_entities(project_id, ad_account_id, level, is_managed, native_entity_id);
