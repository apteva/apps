-- One rate-limit cooldown per provider account (platform:connection:native
-- account), shared by every project bound to it.
CREATE TABLE IF NOT EXISTS ad_provider_cooldowns (
    provider_key TEXT PRIMARY KEY,
    cooldown_until TEXT NOT NULL,
    failure_count INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
