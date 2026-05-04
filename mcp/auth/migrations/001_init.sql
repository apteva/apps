-- Auth v0.1 — identity layer for Apteva-deployed SaaS.
--
-- Every table is project-partitioned (matches CRM's pattern) so the
-- same schema would serve `scope: global` later if we want a shared
-- user pool across projects. v0.1 ships project-only.
--
-- Naming convention:
--   *_hash columns store sha256(token); raw tokens never persist.
--   password_hash is argon2id-encoded (full $argon2id$... string).
--   timestamps are TEXT in RFC3339 / ISO-8601 — sqlite has no native
--     timestamp type, and storing strings keeps go time.Parse honest.

-- ─── users ────────────────────────────────────────────────────────────
-- The identity record. Profile data beyond name/avatar belongs in the
-- consuming SaaS's own DB or the CRM app — auth owns identity, not
-- profile.
CREATE TABLE users (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,

  email                 TEXT NOT NULL,                        -- lower-cased on write
  email_verified_at     TEXT,
  password_hash         TEXT,                                  -- nullable: OAuth-only users have no password
  display_name          TEXT,
  avatar_url            TEXT,

  status                TEXT NOT NULL DEFAULT 'active',        -- active | disabled | deleted
  -- Throttling lives on the user row because it's read on every login.
  failed_login_count    INTEGER NOT NULL DEFAULT 0,
  locked_until          TEXT,
  last_login_at         TEXT,

  created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_users_email   ON users(project_id, email);
CREATE        INDEX ix_users_status  ON users(project_id, status);
CREATE        INDEX ix_users_created ON users(project_id, created_at DESC);

-- ─── clients ─────────────────────────────────────────────────────────
-- The OAuth client registry. One row per frontend / mobile app /
-- backend service / M2M integration. Auth0 calls this "Application",
-- Cognito calls it "App Client" — we use the OAuth2 spec term.
CREATE TABLE clients (
  id                          INTEGER PRIMARY KEY,
  project_id                  TEXT NOT NULL,

  client_id                   TEXT NOT NULL,                   -- public, e.g. "akc_<random>"
  client_secret_hash          TEXT,                            -- null for public (spa | native) clients
  name                        TEXT NOT NULL,
  type                        TEXT NOT NULL,                   -- spa | web | native | m2m

  redirect_uris               TEXT NOT NULL DEFAULT '[]',      -- JSON array of allowed redirect URIs
  allowed_origins             TEXT NOT NULL DEFAULT '[]',      -- JSON array, used for CORS on token endpoints
  allowed_grant_types         TEXT NOT NULL DEFAULT '[]',      -- JSON array: authorization_code, refresh_token, client_credentials, password
  token_endpoint_auth_method  TEXT NOT NULL DEFAULT 'none',    -- none | client_secret_post | client_secret_basic

  require_pkce                INTEGER NOT NULL DEFAULT 1,      -- 1 by default for spa/native; ignored for m2m
  require_mfa                 INTEGER NOT NULL DEFAULT 0,
  jwt_audience                TEXT,                            -- written into `aud` claim; defaults to client_id

  access_token_ttl_seconds    INTEGER,                         -- null = inherit install default
  refresh_token_ttl_seconds   INTEGER,
  refresh_rotation            INTEGER NOT NULL DEFAULT 1,

  disabled_at                 TEXT,
  created_at                  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_clients_client_id ON clients(client_id);
CREATE        INDEX ix_clients_project   ON clients(project_id);

-- ─── oauth_identities ────────────────────────────────────────────────
-- Linkage to external identity providers. A user may have many rows
-- (signed in once with Google, once with GitHub) all pointing at the
-- same user_id.
CREATE TABLE oauth_identities (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT NOT NULL,
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider            TEXT NOT NULL,                          -- google | github | apple | …
  provider_user_id    TEXT NOT NULL,
  raw_profile         TEXT,                                    -- JSON of last fetched profile
  created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_used_at        TEXT
);
CREATE UNIQUE INDEX ux_oauth_id    ON oauth_identities(project_id, provider, provider_user_id);
CREATE        INDEX ix_oauth_user  ON oauth_identities(user_id);

-- ─── sessions ────────────────────────────────────────────────────────
-- Refresh-token state. Access tokens are stateless JWTs and never live
-- here. On refresh the row is rotated: revoked_at set on the old, new
-- row inserted. This defeats refresh-token replay.
CREATE TABLE sessions (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT NOT NULL,
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id           TEXT NOT NULL,                          -- denormalised from clients.client_id
  refresh_token_hash  TEXT NOT NULL,                          -- sha256 of the raw refresh token
  user_agent          TEXT,
  ip                  TEXT,
  created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_seen_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  expires_at          TEXT NOT NULL,
  revoked_at          TEXT
);
CREATE UNIQUE INDEX ux_sessions_token  ON sessions(refresh_token_hash);
CREATE        INDEX ix_sessions_user   ON sessions(user_id, revoked_at);
CREATE        INDEX ix_sessions_expiry ON sessions(expires_at);

-- ─── verification_tokens ─────────────────────────────────────────────
-- Single table for every "click this link" flow. Discriminated by
-- kind. Adding a new flow later = a new kind value, no migration.
CREATE TABLE verification_tokens (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash    TEXT NOT NULL,                                -- sha256 of raw token
  kind          TEXT NOT NULL,                                 -- verify_email | reset_password | magic_link | invite
  meta          TEXT,                                          -- JSON, kind-specific (e.g. new_email for an email-change confirm)
  expires_at    TEXT NOT NULL,
  used_at       TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_vt_token  ON verification_tokens(token_hash);
CREATE        INDEX ix_vt_user   ON verification_tokens(user_id, kind);
CREATE        INDEX ix_vt_expiry ON verification_tokens(expires_at);

-- ─── mfa_factors ─────────────────────────────────────────────────────
-- Second-factor enrollment. v0.1 ships TOTP; the table admits WebAuthn
-- and SMS later without migration.
CREATE TABLE mfa_factors (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT NOT NULL,
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind                TEXT NOT NULL,                          -- totp (v0.1)
  secret_encrypted    TEXT NOT NULL,                          -- base64(aead-encrypted) of the TOTP shared secret
  label               TEXT,                                    -- "iPhone Authenticator", etc.
  confirmed_at        TEXT,                                    -- null until first valid code is entered
  created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_mfa_user ON mfa_factors(user_id, confirmed_at);

-- ─── recovery_codes ──────────────────────────────────────────────────
-- 10 single-use codes generated at MFA enrollment. Survive factor
-- rotation (lost phone → new TOTP → same recovery codes still valid).
CREATE TABLE recovery_codes (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash     TEXT NOT NULL,                                -- sha256 of the raw code (XXXX-XXXX format)
  used_at       TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_rec_user ON recovery_codes(user_id, used_at);

-- ─── signing_keys ────────────────────────────────────────────────────
-- JWT keypair history. Asymmetric (EdDSA) so any consumer can verify
-- offline against /.well-known/jwks.json. Old keys are kept (not
-- deleted) until tokens signed by them have all expired; JWKS keeps
-- publishing them through that drain window.
CREATE TABLE signing_keys (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  kid               TEXT NOT NULL,                            -- key id, lands in JWT header
  alg               TEXT NOT NULL DEFAULT 'EdDSA',
  private_pem       TEXT NOT NULL,                            -- TODO v0.2: encrypt at rest
  public_pem        TEXT NOT NULL,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  retired_at        TEXT                                       -- set when superseded; row stays in JWKS until drained
);
CREATE UNIQUE INDEX ux_sk_kid ON signing_keys(kid);
CREATE        INDEX ix_sk_proj ON signing_keys(project_id, retired_at);

-- ─── audit_log ───────────────────────────────────────────────────────
-- Append-only event trail. Read during incident response. Mutations
-- would defeat the purpose.
CREATE TABLE audit_log (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  user_id       INTEGER,                                      -- null for events without a user (failed login for unknown email)
  client_id     TEXT,                                          -- null when not from an OAuth client (admin actions, etc.)
  event         TEXT NOT NULL,                                -- signup | login | login_failed | logout | password_changed | password_reset_requested | mfa_enrolled | mfa_disabled | oauth_linked | session_revoked | client_secret_rotated | …
  ip            TEXT,
  user_agent    TEXT,
  metadata      TEXT,                                          -- event-specific JSON
  occurred_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_audit_proj_time ON audit_log(project_id, occurred_at DESC);
CREATE INDEX ix_audit_user      ON audit_log(user_id, occurred_at DESC);
CREATE INDEX ix_audit_event     ON audit_log(project_id, event, occurred_at DESC);
