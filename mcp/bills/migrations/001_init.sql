-- Bills v0.1.0.
--
-- Five tables. Mirrors billing's discipline closely:
--   - provider column on bills is set at create + immutable, so v0.2
--     bank-rail integrations (mercury / wise / bill_dot_com) can land
--     alongside existing local rows with no migration.
--   - ux_bill_payments_ext is the unique index that makes v0.2's
--     reconciler / webhook writes idempotent today — the same
--     ON CONFLICT DO NOTHING shape works for both code paths.
--   - audit log is append-only per bill, keyed on bill_id.
--
-- v0.1.0 differences from billing's invoices schema:
--   - bills.vendor_invoice_number is THEIR number (the vendor's), not
--     ours. Held alongside our internal id; the dashboard surfaces
--     both. We don't mint a number on approval — there's no "bill
--     number" we issue.
--   - approved_at + approved_by + scheduled_for are columns directly
--     on bills (single-step workflow); v0.2 will introduce a separate
--     bill_approvals table without touching this schema.
--   - vendors has W-9 / 1099 plumbing fields that customers doesn't.

-- ── vendors ──────────────────────────────────────────────────────────
CREATE TABLE vendors (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  name            TEXT    NOT NULL,
  email           TEXT,
  phone           TEXT,

  -- JSON: {line1, line2, city, state, postal_code, country}
  billing_address TEXT    NOT NULL DEFAULT '{}',
  -- JSON array: [{type:"vat"|"ein"|"gst"|…, value:"…"}]
  tax_ids         TEXT    NOT NULL DEFAULT '[]',

  currency        TEXT,                          -- preferred default for bills

  -- AP-specific fields (no equivalent in billing.customers).
  default_payment_method      TEXT,              -- 'wire' | 'check' | 'ach' | 'card' — pre-fills bills_schedule_payment
  default_payment_terms_days  INTEGER,           -- "Net 30" → 30; per-vendor override of install default
  w9_received_at              TIMESTAMP,         -- US 1099 prerequisite

  -- Bank-rail vendor id, populated lazily on first non-local payment.
  external_id     TEXT,

  metadata        TEXT    NOT NULL DEFAULT '{}',

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at      TIMESTAMP                      -- soft-delete
);

CREATE INDEX ix_vendors_proj  ON vendors(project_id, deleted_at);
CREATE INDEX ix_vendors_email ON vendors(project_id, email);
CREATE INDEX ix_vendors_ext   ON vendors(external_id);


-- ── bills ────────────────────────────────────────────────────────────
CREATE TABLE bills (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  vendor_id         INTEGER NOT NULL REFERENCES vendors(id),

  -- 'local' | 'mercury' | 'wise' | 'bill_dot_com' (v0.2). Frozen after
  -- bills_create — switching means void+recreate. v0.1.0 enforces 'local'.
  provider          TEXT    NOT NULL DEFAULT 'local',

  -- THEIR invoice number (e.g. "INV-2026-0042" as the vendor minted it).
  -- Combined with vendor_id this is the duplicate-entry key — the
  -- ix_bills_vendor_dup index below catches the common mistake of
  -- entering the same bill twice.
  vendor_invoice_number TEXT,
  vendor_invoice_date   TIMESTAMP,               -- when they issued it

  -- received | approved | scheduled | paid | disputed | void
  status            TEXT    NOT NULL DEFAULT 'received',

  currency          TEXT    NOT NULL,            -- ISO 4217

  -- Money is integer cents. Tax is basis points (1000 = 10.00%) on
  -- line items. tax_cents here is INPUT tax (VAT input / sales tax we
  -- paid), distinct from billing.invoices.tax_cents which is OUTPUT
  -- tax we charged. Same column shape; different meaning.
  subtotal_cents    INTEGER NOT NULL DEFAULT 0,
  tax_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents       INTEGER NOT NULL DEFAULT 0,
  amount_paid_cents INTEGER NOT NULL DEFAULT 0,

  due_date          TIMESTAMP,
  notes             TEXT,

  -- AP-specific structured fields.
  category          TEXT,                        -- 'cogs' | 'opex' | 'capex' | 'tax' | other (drives reporting)
  gl_account        TEXT,                        -- chart-of-accounts code, free-form for v0.1
  attached_file_id  INTEGER,                     -- storage app file_id of the original PDF/email

  -- Workflow timestamps.
  approved_at       TIMESTAMP,
  approved_by       TEXT,                        -- 'agent:<id>' | 'human:<id>'
  scheduled_for     TIMESTAMP,                   -- when payment is/was scheduled
  scheduled_method  TEXT,                        -- 'wire' | 'check' | 'ach' | 'card'

  -- Bank-rail bookkeeping (v0.2+). Stay NULL on local-provider bills.
  external_id       TEXT,
  external_url      TEXT,
  last_synced_at    TIMESTAMP,

  metadata          TEXT    NOT NULL DEFAULT '{}',

  paid_at           TIMESTAMP,
  voided_at         TIMESTAMP,
  disputed_at       TIMESTAMP,
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at        TIMESTAMP
);

CREATE INDEX ix_bills_proj     ON bills(project_id, deleted_at);
CREATE INDEX ix_bills_vendor   ON bills(vendor_id, status);
CREATE INDEX ix_bills_status   ON bills(project_id, status, updated_at DESC);
CREATE INDEX ix_bills_due      ON bills(project_id, due_date) WHERE status IN ('approved', 'scheduled');
CREATE INDEX ix_bills_provider ON bills(project_id, provider, status);
CREATE INDEX ix_bills_ext      ON bills(external_id);

-- v0.2 reconciler walks this. Index now to keep the migration tidy.
CREATE INDEX ix_bills_sync ON bills(provider, status, last_synced_at)
  WHERE provider != 'local' AND status IN ('scheduled', 'paid');

-- Duplicate-entry catcher: same vendor + same invoice number twice
-- almost always means somebody re-keyed an existing bill. Partial
-- index so NULL vendor_invoice_number (rare but allowed) doesn't
-- trip it.
CREATE UNIQUE INDEX ux_bills_vendor_invnum
  ON bills(project_id, vendor_id, vendor_invoice_number)
  WHERE vendor_invoice_number IS NOT NULL AND deleted_at IS NULL;


-- ── bill_line_items ─────────────────────────────────────────────────
CREATE TABLE bill_line_items (
  id               INTEGER PRIMARY KEY,
  bill_id          INTEGER NOT NULL REFERENCES bills(id) ON DELETE CASCADE,

  position         INTEGER NOT NULL,             -- render order

  description      TEXT    NOT NULL,
  quantity         REAL    NOT NULL DEFAULT 1,
  unit_price_cents INTEGER NOT NULL,
  amount_cents     INTEGER NOT NULL,             -- denormalised: round(quantity * unit_price)
  tax_rate_bps     INTEGER NOT NULL DEFAULT 0,   -- basis points; INPUT tax we were charged

  external_id      TEXT,                         -- bank-rail line id (v0.2+)
  metadata         TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX ix_bill_line_items_bill ON bill_line_items(bill_id, position);


-- ── bill_payments ───────────────────────────────────────────────────
CREATE TABLE bill_payments (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  bill_id      INTEGER REFERENCES bills(id),
  vendor_id    INTEGER NOT NULL REFERENCES vendors(id),

  amount_cents INTEGER NOT NULL,                 -- always positive — refunds-from-vendor are a bill v0.2 concept

  currency     TEXT    NOT NULL,

  -- 'wire' | 'check' | 'cash' | 'ach' | 'card' | 'other'.
  -- v0.1.0 rejects 'external_rail' — reserved for v0.2 bank integrations.
  method       TEXT    NOT NULL,

  -- Bank-rail transaction id when method='external_rail'. The unique
  -- index makes inserts idempotent — v0.2's reconciler / webhooks
  -- write through it.
  external_id  TEXT,

  sent_at      TIMESTAMP NOT NULL,               -- when the money left our account
  notes        TEXT,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_bill_payments_proj    ON bill_payments(project_id, sent_at DESC);
CREATE INDEX ix_bill_payments_bill    ON bill_payments(bill_id);
CREATE INDEX ix_bill_payments_vendor  ON bill_payments(vendor_id, sent_at DESC);

CREATE UNIQUE INDEX ux_bill_payments_ext ON bill_payments(method, external_id)
  WHERE external_id IS NOT NULL;


-- ── bill_audit_log ──────────────────────────────────────────────────
CREATE TABLE bill_audit_log (
  id          INTEGER PRIMARY KEY,
  bill_id     INTEGER NOT NULL REFERENCES bills(id) ON DELETE CASCADE,

  -- 'agent:<id>' | 'human:<id>' | 'system:reconciler'
  actor       TEXT    NOT NULL,
  -- 'create' | 'update' | 'approve' | 'reject' | 'schedule' | 'paid' |
  -- 'partial_payment' | 'void' | 'sync' | 'add_line_item'
  action      TEXT    NOT NULL,
  details     TEXT    NOT NULL DEFAULT '{}',     -- JSON

  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_bill_audit_bill ON bill_audit_log(bill_id, created_at DESC);
