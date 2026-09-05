-- Existing JWTs have no logical session ID. Force reauthentication at this
-- security boundary; invalidate recovery credentials previously logged in clear.
UPDATE users SET authorization_version = authorization_version + 1;
UPDATE sessions SET revoked_at = COALESCE(revoked_at, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
UPDATE verification_tokens SET used_at = COALESCE(used_at, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
UPDATE audit_log SET metadata = CASE WHEN json_valid(metadata) THEN json_remove(metadata,'$.link') ELSE NULL END
 WHERE event IN ('verify_email_sent','password_reset_sent');

CREATE TABLE auth_session_families (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 organization_id INTEGER NOT NULL REFERENCES organizations(id),
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 client_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_seen_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 revoked_at TEXT
);
CREATE INDEX ix_auth_families_user ON auth_session_families(project_id,organization_id,user_id,revoked_at);
CREATE INDEX ix_auth_families_expiry ON auth_session_families(expires_at);
ALTER TABLE sessions ADD COLUMN family_id TEXT REFERENCES auth_session_families(id) ON DELETE CASCADE;
CREATE INDEX ix_sessions_family ON sessions(family_id);
CREATE TABLE auth_rate_limits (
 key TEXT PRIMARY KEY,
 count INTEGER NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE INDEX ix_auth_rate_expiry ON auth_rate_limits(expires_at);
CREATE INDEX ix_audit_client_event ON audit_log(project_id,client_id,event,occurred_at);
ALTER TABLE signing_keys ADD COLUMN verify_until TEXT;

-- Queue mailbox work without storing the raw recovery credential. The worker
-- generates that credential only when attempting delivery.
CREATE TABLE auth_recovery_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 organization_id INTEGER NOT NULL REFERENCES organizations(id),
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 client_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('verify_email','reset_password')),
 continue_url TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(project_id,organization_id,user_id,client_id,kind)
);
CREATE INDEX ix_auth_recovery_due ON auth_recovery_jobs(next_attempt);
CREATE INDEX ix_users_org_page ON users(project_id,organization_id,id DESC);
CREATE INDEX ix_users_project_page ON users(project_id,id DESC);
