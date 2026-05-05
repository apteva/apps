-- Billing v0.1.0.
--
-- Five tables. provider column on invoices is the single most
-- important forward-compat decision: it's set at create time and
-- frozen, so v0.1.1 can flip an install's Stripe wiring on without
-- migrating any existing rows. ux_payments_ext is the second:
-- v0.1.0's reconciler-shaped writes use ON CONFLICT DO NOTHING
-- against it; v0.1.1 webhooks reuse the exact same insert, so no
-- double-processing when both code paths are live.

-- ── customers ────────────────────────────────────────────────────────
CREATE TABLE customers (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  name            TEXT    NOT NULL,
  email           TEXT,
  phone           TEXT,

  -- JSON: {line1, line2, city, state, postal_code, country}
  billing_address TEXT    NOT NULL DEFAULT '{}',
  -- JSON array: [{type:"vat"|"ein"|…, value:"…"}]
  tax_ids         TEXT    NOT NULL DEFAULT '[]',

  currency        TEXT,                          -- preferred default for invoices

  -- Stripe customer id, populated lazily on first stripe-provider
  -- finalize. Stays NULL on local-only installs forever.
  external_id     TEXT,

  metadata        TEXT    NOT NULL DEFAULT '{}',

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at      TIMESTAMP                      -- soft-delete
);

CREATE INDEX ix_customers_proj  ON customers(project_id, deleted_at);
CREATE INDEX ix_customers_email ON customers(project_id, email);
CREATE INDEX ix_customers_ext   ON customers(external_id);


-- ── invoices ─────────────────────────────────────────────────────────
CREATE TABLE invoices (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  customer_id       INTEGER NOT NULL REFERENCES customers(id),

  -- 'local' | 'stripe'. Frozen after invoices_create — switching
  -- means delete+recreate. v0.1.0 enforces 'local' at create time.
  provider          TEXT    NOT NULL DEFAULT 'local',

  -- Minted on finalize from invoice_number_format. NULL while draft.
  number            TEXT,

  -- draft | open | paid | void | uncollectible
  status            TEXT    NOT NULL DEFAULT 'draft',

  currency          TEXT    NOT NULL,            -- ISO 4217

  -- Money is integer cents. Tax is basis points (1000 = 10.00%) on
  -- line items; invoices store the rolled-up dollar tax in cents.
  subtotal_cents    INTEGER NOT NULL DEFAULT 0,
  tax_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents       INTEGER NOT NULL DEFAULT 0,
  amount_paid_cents INTEGER NOT NULL DEFAULT 0,

  due_date          TIMESTAMP,
  notes             TEXT,

  -- Populated only when provider='stripe' (v0.1.1+). Stay NULL
  -- in v0.1.0 — the schema is forward-compat-ready.
  external_id       TEXT,                        -- stripe invoice id
  external_url      TEXT,                        -- stripe hosted invoice url
  last_synced_at    TIMESTAMP,                   -- last successful reconciler pull

  metadata          TEXT    NOT NULL DEFAULT '{}',

  finalized_at      TIMESTAMP,
  paid_at           TIMESTAMP,
  voided_at         TIMESTAMP,
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at        TIMESTAMP
);

CREATE INDEX ix_invoices_proj     ON invoices(project_id, deleted_at);
CREATE INDEX ix_invoices_cust     ON invoices(customer_id, status);
CREATE INDEX ix_invoices_status   ON invoices(project_id, status, updated_at DESC);
CREATE INDEX ix_invoices_provider ON invoices(project_id, provider, status);
CREATE INDEX ix_invoices_ext      ON invoices(external_id);

-- v0.1.1's reconciler walks this. v0.1.0 doesn't query it but
-- creating the index now keeps the migration tidy.
CREATE INDEX ix_invoices_sync ON invoices(provider, status, last_synced_at)
  WHERE provider = 'stripe' AND status = 'open';

-- Project-scoped invoice numbers. Partial index so drafts (number
-- IS NULL) don't collide.
CREATE UNIQUE INDEX ux_invoices_number ON invoices(project_id, number)
  WHERE number IS NOT NULL;


-- ── invoice_line_items ───────────────────────────────────────────────
CREATE TABLE invoice_line_items (
  id               INTEGER PRIMARY KEY,
  invoice_id       INTEGER NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,

  position         INTEGER NOT NULL,             -- render order

  description      TEXT    NOT NULL,
  quantity         REAL    NOT NULL DEFAULT 1,
  unit_price_cents INTEGER NOT NULL,
  amount_cents     INTEGER NOT NULL,             -- denormalised: round(quantity * unit_price)
  tax_rate_bps     INTEGER NOT NULL DEFAULT 0,   -- basis points; 1000 = 10.00%

  external_id      TEXT,                         -- stripe line item id (v0.1.1+)
  metadata         TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX ix_line_items_invoice ON invoice_line_items(invoice_id, position);


-- ── payments ─────────────────────────────────────────────────────────
CREATE TABLE payments (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  invoice_id   INTEGER REFERENCES invoices(id),
  customer_id  INTEGER NOT NULL REFERENCES customers(id),

  amount_cents INTEGER NOT NULL,                 -- negative for refunds

  currency     TEXT    NOT NULL,

  -- 'stripe' | 'wire' | 'cash' | 'check' | 'other'.
  -- v0.1.0 rejects 'stripe' from payments_record — that path is
  -- reserved for the v0.1.1 reconciler.
  method       TEXT    NOT NULL,

  -- Stripe payment_intent / charge id when method='stripe'. The
  -- unique index below makes inserts idempotent — the reconciler
  -- (v0.1.1) and webhook handler (v0.2) both write through it.
  external_id  TEXT,

  received_at  TIMESTAMP NOT NULL,
  notes        TEXT,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_payments_proj    ON payments(project_id, received_at DESC);
CREATE INDEX ix_payments_invoice ON payments(invoice_id);

-- Idempotency for stripe-side payment writes. Unused in v0.1.0 (no
-- writes set external_id) but creating it now means v0.1.1's
-- reconciler — and v0.2's webhook handler — can use the same
-- ON CONFLICT DO NOTHING shape with no schema change.
CREATE UNIQUE INDEX ux_payments_ext ON payments(method, external_id)
  WHERE external_id IS NOT NULL;


-- ── invoice_audit_log ────────────────────────────────────────────────
CREATE TABLE invoice_audit_log (
  id          INTEGER PRIMARY KEY,
  invoice_id  INTEGER NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,

  -- 'agent:<id>' | 'human:<id>' | 'system:reconciler'
  actor       TEXT    NOT NULL,
  -- 'create' | 'finalize' | 'void' | 'paid' | 'partial_payment' | 'sync' | 'add_line_item'
  action      TEXT    NOT NULL,
  details     TEXT    NOT NULL DEFAULT '{}',     -- JSON

  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_audit_invoice ON invoice_audit_log(invoice_id, created_at DESC);
