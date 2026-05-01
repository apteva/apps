-- Page-level access tokens for platforms that need a per-destination
-- token to write (Facebook /feed: error 210 without it; Instagram +
-- LinkedIn pages may follow). The user-level token in the connection
-- only grants list-pages and similar reads.
--
-- Stored as JSON to keep the schema flexible for whatever blob each
-- upstream returns (FB returns a string token; some APIs add expiry +
-- scope). Empty string when the platform doesn't need a separate
-- token (Twitter, TikTok, single-OAuth-token APIs).
ALTER TABLE social_accounts ADD COLUMN page_credentials TEXT NOT NULL DEFAULT '';
