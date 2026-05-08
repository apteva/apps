-- ads v0.1 — connected ad accounts.
--
-- Design:
--   - This app is a thin control plane over per-platform ad APIs. The
--     only durable state is the mapping from a local ad_account id (the
--     handle agents pass to MCP tools) to the underlying connection +
--     native platform account id (e.g. Meta's act_123456789).
--   - All campaign / ad set / ad / creative / audience state lives
--     upstream on the ad platform. Tools proxy through to the bound
--     integration via PlatformAPI.ExecuteIntegrationTool. We do NOT
--     shadow upstream rows locally — too much drift, no benefit at
--     this scale.
--   - pending_accounts mirrors social: holds OAuth-in-flight rows so the
--     OAuth callback page can graduate one to ad_accounts after the
--     user picks an ad account from the upstream account list.

CREATE TABLE IF NOT EXISTS pending_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,                  -- meta (= facebook + instagram); future: google, twitter
    integration_slug TEXT NOT NULL,          -- "facebook-ads", "google-ads", "twitter-ads"
    connection_id INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,                    -- pending_oauth | ready | finalized | expired
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pending_project ON pending_accounts(project_id, status);
CREATE INDEX IF NOT EXISTS idx_pending_conn ON pending_accounts(connection_id);

CREATE TABLE IF NOT EXISTS ad_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,                  -- meta | google | twitter
    connection_id INTEGER NOT NULL,
    native_account_id TEXT NOT NULL,         -- Meta act_*, Google customers/123, X account id
    display_name TEXT NOT NULL,
    currency TEXT,                           -- ISO 4217 (USD, EUR, …) when available
    timezone_name TEXT,
    status TEXT NOT NULL DEFAULT 'active',   -- active | needs_reauth | disconnected
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ad_accounts_project ON ad_accounts(project_id, status);
CREATE INDEX IF NOT EXISTS idx_ad_accounts_conn ON ad_accounts(connection_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ad_accounts_native
    ON ad_accounts(project_id, platform, native_account_id);
