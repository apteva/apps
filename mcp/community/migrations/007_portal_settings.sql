-- Community v0.8: customer-facing portal settings live on the community
-- tenancy row. A single Community install may therefore host independently
-- branded communities with isolated Auth organizations and SPA clients.

ALTER TABLE communities ADD COLUMN auth_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN auth_organization_id TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN auth_organization_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN brand_name TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN logo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN favicon_url TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN primary_color TEXT NOT NULL DEFAULT '#6d5dfc';
ALTER TABLE communities ADD COLUMN accent_color TEXT NOT NULL DEFAULT '#22c55e';
ALTER TABLE communities ADD COLUMN support_email TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN portal_host TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN signup_mode TEXT NOT NULL DEFAULT 'open'
    CHECK(signup_mode IN ('open', 'closed'));
ALTER TABLE communities ADD COLUMN auto_create_members INTEGER NOT NULL DEFAULT 1;
