-- fleet 007: scoped bearer credentials for delegated provider execution.

ALTER TABLE fleet_provider_grants ADD COLUMN token_hash TEXT NOT NULL DEFAULT '';
