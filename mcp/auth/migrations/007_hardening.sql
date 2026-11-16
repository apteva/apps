-- Email verification provenance. email_verified_at now means the mailbox was
-- proven (or an admin asserted it); signups made while verification was
-- switched off are recorded as not_required instead of fake-verified.
ALTER TABLE users ADD COLUMN email_verification TEXT NOT NULL DEFAULT 'pending';
UPDATE users SET email_verification = 'verified' WHERE email_verified_at IS NOT NULL;
-- Before 0.14.0 a signup with verification off stamped email_verified_at with
-- the creation time. The signup audit row proves where those rows came from.
UPDATE users SET email_verification = 'not_required', email_verified_at = NULL
 WHERE email_verified_at IS NOT NULL AND email_verified_at = created_at AND kind = 'account'
   AND EXISTS (SELECT 1 FROM audit_log a WHERE a.project_id = users.project_id AND a.user_id = users.id AND a.event = 'signup');
UPDATE users SET email_verification = 'admin_asserted'
 WHERE email_verification = 'verified' AND email_verified_at = created_at
   AND EXISTS (SELECT 1 FROM audit_log a WHERE a.project_id = users.project_id AND a.user_id = users.id AND a.event = 'user_created_admin');
-- Creation-time stamps without surviving audit evidence (retention pruned it)
-- keep working but are labelled so admins can tell them from proven mailboxes.
UPDATE users SET email_verification = 'legacy_assumed'
 WHERE email_verification = 'verified' AND email_verified_at = created_at;
CREATE INDEX ix_users_email_verification ON users(project_id, organization_id, email_verification);

-- Audit attribution: who acted, through which app and credential, whether it
-- succeeded, and a correlation ID that ties together one request's rows.
ALTER TABLE audit_log ADD COLUMN actor_type TEXT;
ALTER TABLE audit_log ADD COLUMN actor_id TEXT;
ALTER TABLE audit_log ADD COLUMN source_app TEXT;
ALTER TABLE audit_log ADD COLUMN source_install_id INTEGER;
ALTER TABLE audit_log ADD COLUMN credential_id TEXT;
ALTER TABLE audit_log ADD COLUMN result TEXT;
ALTER TABLE audit_log ADD COLUMN correlation_id TEXT;
CREATE INDEX ix_audit_actor ON audit_log(project_id, actor_type, actor_id, occurred_at);
CREATE INDEX ix_audit_correlation ON audit_log(correlation_id);
