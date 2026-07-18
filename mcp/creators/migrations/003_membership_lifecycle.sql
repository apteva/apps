-- Membership payment lifecycle, renewable portal credentials, and idempotency.

ALTER TABLE members ADD COLUMN portal_token_expires_at TEXT;
ALTER TABLE members ADD COLUMN portal_token_revoked_at TEXT;

UPDATE members
SET portal_token_expires_at = datetime('now', '+90 days')
WHERE portal_token_expires_at IS NULL;

CREATE TABLE IF NOT EXISTS membership_payments (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id         TEXT NOT NULL,
  space_id           INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  member_id          INTEGER NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  tier_id            INTEGER NOT NULL REFERENCES tiers(id) ON DELETE RESTRICT,
  billing_invoice_id INTEGER,
  idempotency_key    TEXT NOT NULL,
  status             TEXT NOT NULL DEFAULT 'creating'
                     CHECK(status IN ('creating','open','paid','failed','void')),
  period_count       INTEGER NOT NULL DEFAULT 1,
  amount_cents       INTEGER NOT NULL,
  currency           TEXT NOT NULL,
  period_start       TEXT,
  period_end         TEXT,
  paid_at            TEXT,
  created_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, space_id, idempotency_key),
  UNIQUE(billing_invoice_id)
);

CREATE INDEX IF NOT EXISTS ix_membership_payments_invoice
  ON membership_payments(billing_invoice_id);
CREATE INDEX IF NOT EXISTS ix_membership_payments_member
  ON membership_payments(project_id, space_id, member_id, created_at DESC);
