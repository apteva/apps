-- Entitlements v0.3.0 -- generic prepaid credit ledger and reservations.

CREATE TABLE credit_transactions (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subject_type          TEXT    NOT NULL DEFAULT 'customer',
  subject_id            TEXT    NOT NULL,
  feature_key           TEXT    NOT NULL,
  amount                INTEGER NOT NULL,
  kind                  TEXT    NOT NULL,
  source_type           TEXT    NOT NULL DEFAULT 'manual',
  source_id             TEXT,
  idempotency_key       TEXT,
  metadata              TEXT    NOT NULL DEFAULT '{}',
  occurred_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK (amount != 0),
  CHECK (kind IN ('grant', 'debit', 'refund', 'adjustment', 'expiry'))
);

CREATE UNIQUE INDEX ux_credit_transactions_idempotency
  ON credit_transactions(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX ix_credit_transactions_subject
  ON credit_transactions(project_id, subject_type, subject_id, feature_key, occurred_at DESC, id DESC);
CREATE INDEX ix_credit_transactions_source
  ON credit_transactions(project_id, source_type, source_id);

CREATE TABLE credit_reservations (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subject_type          TEXT    NOT NULL DEFAULT 'customer',
  subject_id            TEXT    NOT NULL,
  feature_key           TEXT    NOT NULL,
  amount                INTEGER NOT NULL,
  committed_amount      INTEGER NOT NULL DEFAULT 0,
  status                TEXT    NOT NULL DEFAULT 'active',
  idempotency_key       TEXT,
  source_type           TEXT    NOT NULL DEFAULT 'manual',
  source_id             TEXT,
  metadata              TEXT    NOT NULL DEFAULT '{}',
  expires_at            TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK (amount > 0),
  CHECK (committed_amount >= 0 AND committed_amount <= amount),
  CHECK (status IN ('active', 'committed', 'released', 'expired'))
);

CREATE UNIQUE INDEX ux_credit_reservations_idempotency
  ON credit_reservations(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX ix_credit_reservations_subject
  ON credit_reservations(project_id, subject_type, subject_id, feature_key, status, expires_at);
