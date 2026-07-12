-- Billing hardening: project-partition issuer identity and persist every
-- Checkout Session that can later generate a payment webhook.

ALTER TABLE issuer_settings RENAME TO issuer_settings_legacy;

CREATE TABLE issuer_settings (
  project_id      TEXT PRIMARY KEY,
  display_name    TEXT    NOT NULL DEFAULT '',
  legal_name      TEXT,
  email           TEXT,
  phone           TEXT,
  website         TEXT,
  brand_color     TEXT,
  address         TEXT    NOT NULL DEFAULT '{}',
  tax_ids         TEXT    NOT NULL DEFAULT '[]',
  bank            TEXT    NOT NULL DEFAULT '{}',
  footer_text     TEXT,
  default_terms   TEXT,
  metadata        TEXT    NOT NULL DEFAULT '{}',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Preserve an existing project-scoped install's issuer as a legacy fallback.
-- Global installs deliberately do not expose this row across projects; each
-- project must write its own issuer identity after upgrade.
INSERT INTO issuer_settings (
  project_id, display_name, legal_name, email, phone, website, brand_color,
  address, tax_ids, bank, footer_text, default_terms, metadata,
  created_at, updated_at
)
SELECT '', display_name, legal_name, email, phone, website, brand_color,
       address, tax_ids, bank, footer_text, default_terms, metadata,
       created_at, updated_at
FROM issuer_settings_legacy
WHERE id = 1;

DROP TABLE issuer_settings_legacy;

CREATE TABLE billing_checkout_sessions (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  invoice_id          INTEGER NOT NULL REFERENCES invoices(id),
  provider            TEXT    NOT NULL DEFAULT 'stripe',
  provider_session_id TEXT    NOT NULL,
  amount_cents        INTEGER NOT NULL,
  currency            TEXT    NOT NULL,
  status              TEXT    NOT NULL DEFAULT 'pending',
  expires_at          TIMESTAMP,
  completed_at        TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_billing_checkout_provider_session
  ON billing_checkout_sessions(provider, provider_session_id);

CREATE INDEX ix_billing_checkout_invoice
  ON billing_checkout_sessions(project_id, invoice_id, status, created_at DESC);

-- Preserve the latest still-payable link from pre-hardening releases. Older
-- links beyond the one stored on invoices cannot be reconstructed locally.
INSERT OR IGNORE INTO billing_checkout_sessions (
  project_id, invoice_id, provider, provider_session_id,
  amount_cents, currency, status, created_at
)
SELECT project_id, id, 'stripe', external_id,
       total_cents - amount_paid_cents, currency, 'pending',
       COALESCE(last_synced_at, created_at)
FROM invoices
WHERE external_id LIKE 'cs_%'
  AND status IN ('open', 'uncollectible')
  AND total_cents > amount_paid_cents;
