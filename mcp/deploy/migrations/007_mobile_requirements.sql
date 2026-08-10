ALTER TABLE mobile_signing_setups ADD COLUMN required_features_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE mobile_signing_setups ADD COLUMN provisioned_features_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE mobile_signing_setups ADD COLUMN managed_features_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE mobile_signing_setups ADD COLUMN requirements_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE mobile_signing_setups ADD COLUMN platform_state_json TEXT NOT NULL DEFAULT '{}';
