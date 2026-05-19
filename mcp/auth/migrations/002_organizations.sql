-- 002_organizations — row-level Organization partition.
--
-- Why: one Apteva project owns one auth install, but operators may
-- run several SaaS in that project that each want their own user pool
-- (Alice@SaaS-A is a different person from Alice@SaaS-B). This adds
-- an Organization layer (Auth0 "Organization" / Clerk "Organization" /
-- WorkOS / Stytch B2B convention) so users / clients / sessions /
-- signing keys / audit / MFA are all org-partitioned.
--
-- Migration shape: additive only. Every user-facing table gains a
-- nullable `organization_id` column; we backfill one "Default" org
-- per project and assign every existing row to it; we replace the
-- UNIQUE indexes that need to scope to org. NOT NULL is enforced at
-- the app layer (db.go) for v0.4.0 — a later patch can tighten the
-- DB-level constraints once we're confident no code path leaks NULL.
--
-- Existing SaaS callers see zero breakage: client_id → organization
-- is a server-side lookup; they keep sending the same client_id and
-- get scoped to the same data they had pre-migration.

-- ─── organizations ───────────────────────────────────────────────────
CREATE TABLE organizations (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  slug              TEXT NOT NULL,                              -- url-safe ("acme", "internal")
  name              TEXT NOT NULL,
  color             TEXT,                                       -- optional hex for UI ("#3b82f6")
  status            TEXT NOT NULL DEFAULT 'active',             -- active | archived
  policy_overrides  TEXT,                                       -- JSON, nullable; nil = inherit install defaults
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_orgs_slug   ON organizations(project_id, slug);
CREATE        INDEX ix_orgs_status ON organizations(project_id, status);

-- Backfill: one Default org per distinct project_id seen across any
-- existing tenant-bearing row. Operators rename via the panel after
-- upgrade. Slug is deliberately the literal string `default` so it's
-- predictable for tools / scripts targeting upgraded installs.
INSERT INTO organizations (project_id, slug, name, color, status)
SELECT DISTINCT project_id, 'default', 'Default', '#94a3b8', 'active'
FROM (
  SELECT project_id FROM users
  UNION SELECT project_id FROM clients
  UNION SELECT project_id FROM signing_keys
  UNION SELECT project_id FROM audit_log
);

-- ─── Add organization_id to every user-facing table ──────────────────
-- Nullable for now; backfilled below. SQLite ALTER TABLE ADD COLUMN
-- can't take a FK clause that references a not-yet-indexed column
-- chain, so we add the column unconstrained — the FK guarantee is
-- enforced by the app layer.
ALTER TABLE users               ADD COLUMN organization_id INTEGER;
ALTER TABLE clients             ADD COLUMN organization_id INTEGER;
ALTER TABLE oauth_identities    ADD COLUMN organization_id INTEGER;
ALTER TABLE sessions            ADD COLUMN organization_id INTEGER;
ALTER TABLE verification_tokens ADD COLUMN organization_id INTEGER;
ALTER TABLE mfa_factors         ADD COLUMN organization_id INTEGER;
ALTER TABLE recovery_codes      ADD COLUMN organization_id INTEGER;
ALTER TABLE signing_keys        ADD COLUMN organization_id INTEGER;
ALTER TABLE audit_log           ADD COLUMN organization_id INTEGER;  -- nullable: project-wide events stay null

-- Backfill every existing row to the Default org for its project_id.
UPDATE users               SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = users.project_id               AND o.slug = 'default');
UPDATE clients             SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = clients.project_id             AND o.slug = 'default');
UPDATE oauth_identities    SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = oauth_identities.project_id    AND o.slug = 'default');
UPDATE sessions            SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = sessions.project_id            AND o.slug = 'default');
UPDATE verification_tokens SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = verification_tokens.project_id AND o.slug = 'default');
UPDATE mfa_factors         SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = mfa_factors.project_id         AND o.slug = 'default');
UPDATE recovery_codes      SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = recovery_codes.project_id      AND o.slug = 'default');
UPDATE signing_keys        SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = signing_keys.project_id        AND o.slug = 'default');
UPDATE audit_log           SET organization_id = (SELECT id FROM organizations o WHERE o.project_id = audit_log.project_id           AND o.slug = 'default');

-- ─── Replace org-scoped UNIQUE indexes ───────────────────────────────
-- ux_users_email was (project_id, email) — must include org now or
-- alice@example.com couldn't exist in two orgs of the same project.
DROP   INDEX ux_users_email;
CREATE UNIQUE INDEX ux_users_email ON users(project_id, organization_id, email);

-- ux_clients_client_id stays globally unique — client_id is the
-- spec-level identifier the SaaS frontend sends, and it's a slug
-- generated with 16 bytes of entropy so cross-org collisions are
-- not a concern. The org lookup happens via the row, not the index.
-- (No change needed.)

-- ux_oauth_id was (project_id, provider, provider_user_id). External
-- providers issue user_ids that are unique per provider — Google's
-- sub for alice@gmail is the same regardless of which org she signs
-- into. To support Alice signing in to two orgs with the same Google
-- account we'd need org scoping here too; for v0.4.0 we keep it
-- (project_id, provider, provider_user_id) and rely on the fact that
-- the same Google id mapping to two users (one per org) is fine —
-- but a single org can't have two users for the same Google account.
-- We add the org-scoped variant alongside without dropping the old:
CREATE UNIQUE INDEX ux_oauth_id_org ON oauth_identities(project_id, organization_id, provider, provider_user_id);
DROP   INDEX ux_oauth_id;

-- ─── New helper indexes ──────────────────────────────────────────────
CREATE INDEX ix_users_status_org   ON users(project_id, organization_id, status);
CREATE INDEX ix_users_created_org  ON users(project_id, organization_id, created_at DESC);
CREATE INDEX ix_audit_org_time     ON audit_log(project_id, organization_id, occurred_at DESC);
CREATE INDEX ix_sk_org             ON signing_keys(project_id, organization_id, retired_at);
CREATE INDEX ix_sessions_org_user  ON sessions(organization_id, user_id, revoked_at);
