-- Checkout v0.1.0 — carts + sessions.
--
-- Three tables that together implement "browse, fill cart, place
-- order":
--
--   carts             — long-lived basket state; one per session_token
--                       (guest) or customer_id (logged in).
--   cart_items        — line items inside a cart, snapshotted from a
--                       catalog price at add-time so the cart total
--                       is stable even if catalog pricing changes.
--   checkout_sessions — one row per "attempt to pay" for a cart. A
--                       cart can have multiple sessions (abandon +
--                       retry). Tracks captured buyer info + the
--                       resulting billing invoice on success.
--
-- v0.1.0 does NOT include Stripe — the pay flow creates a billing
-- invoice (draft → finalize) and returns it for manual payment via
-- billing's existing flow. Schema is forward-compat: provider /
-- provider_session_id / processed_event_ids land cleanly when
-- billing's Stripe integration (v0.8.0) is wired in checkout v0.2.0.

-- ── carts ────────────────────────────────────────────────────────────
CREATE TABLE carts (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  -- Exactly one of these is set. Guest carts use a server-issued
  -- opaque token (passed back to the storefront as a cookie value);
  -- authenticated carts use the billing customer id.
  session_token   TEXT,
  customer_id     INTEGER,

  -- Materialised totals — recomputed on every cart_items mutation
  -- so the cart-summary widget renders without a join.
  subtotal_cents  INTEGER NOT NULL DEFAULT 0,
  currency        TEXT    NOT NULL DEFAULT 'USD',
  item_count      INTEGER NOT NULL DEFAULT 0,

  -- 'open' (mutable, customer is shopping)
  -- 'checkout' (locked while a checkout_session is awaiting payment)
  -- 'converted' (linked to invoice_id; terminal)
  -- 'abandoned' (TTL hit; terminal)
  status          TEXT    NOT NULL DEFAULT 'open',

  invoice_id      INTEGER,                              -- billing invoice on conversion

  metadata        TEXT    NOT NULL DEFAULT '{}',

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at      TIMESTAMP                             -- TTL for guest carts
);

-- One OPEN cart per session_token. Partial index so a session can
-- start a new cart after the previous one converted/abandoned.
CREATE UNIQUE INDEX ux_carts_session
  ON carts(project_id, session_token)
  WHERE session_token IS NOT NULL AND status = 'open';
CREATE INDEX ix_carts_customer  ON carts(project_id, customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX ix_carts_status    ON carts(project_id, status, updated_at);
CREATE INDEX ix_carts_expiring  ON carts(expires_at) WHERE status = 'open';


-- ── cart_items ───────────────────────────────────────────────────────
CREATE TABLE cart_items (
  id                INTEGER PRIMARY KEY,
  cart_id           INTEGER NOT NULL REFERENCES carts(id) ON DELETE CASCADE,

  -- Cross-app FKs into catalog (convention; SQLite can't enforce).
  price_id          INTEGER NOT NULL,
  product_id        INTEGER NOT NULL,

  -- Snapshot fields — frozen at add-time so the cart total the
  -- customer sees doesn't shift if catalog prices change mid-session.
  description       TEXT    NOT NULL,
  unit_amount_cents INTEGER NOT NULL,
  currency          TEXT    NOT NULL,
  quantity          REAL    NOT NULL DEFAULT 1,

  metadata          TEXT    NOT NULL DEFAULT '{}',
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Adding the same price again increments the existing row's qty
-- rather than creating a duplicate.
CREATE UNIQUE INDEX ux_cart_items_price ON cart_items(cart_id, price_id);
CREATE INDEX ix_cart_items_cart ON cart_items(cart_id);


-- ── checkout_sessions ────────────────────────────────────────────────
CREATE TABLE checkout_sessions (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  cart_id             INTEGER NOT NULL REFERENCES carts(id),

  -- 'manual' (v0.1.0 — creates draft invoice, user records payment)
  -- 'stripe' (v0.2.0+ — creates Stripe Checkout Session, webhook
  --           promotes session→paid + invoice→paid)
  provider            TEXT    NOT NULL DEFAULT 'manual',
  provider_session_id TEXT,                                  -- Stripe cs_… id (v0.2.0+)

  -- Buyer info captured during checkout. Pushed to billing via
  -- customers_upsert_by_email on conversion.
  email               TEXT,
  customer_name       TEXT,
  shipping_address    TEXT    NOT NULL DEFAULT '{}',         -- JSON
  billing_address     TEXT    NOT NULL DEFAULT '{}',         -- JSON

  -- 'started'           — created, accepting field updates
  -- 'awaiting_payment'  — locked, invoice created, payment expected
  -- 'paid'              — terminal success; cart converted
  -- 'cancelled'         — admin or buyer cancellation
  -- 'expired'           — TTL hit
  status              TEXT    NOT NULL DEFAULT 'started',

  invoice_id          INTEGER,                               -- billing invoice once created

  -- Materialised totals frozen at the moment payment was offered.
  subtotal_cents      INTEGER NOT NULL DEFAULT 0,
  tax_cents           INTEGER NOT NULL DEFAULT 0,
  total_cents         INTEGER NOT NULL DEFAULT 0,
  currency            TEXT    NOT NULL,

  -- Stripe webhook idempotency (v0.2.0+). Stays empty in v0.1.0.
  processed_event_ids TEXT    NOT NULL DEFAULT '[]',         -- JSON array of strings

  metadata            TEXT    NOT NULL DEFAULT '{}',

  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at        TIMESTAMP,
  expires_at          TIMESTAMP                              -- typically now() + 30 min
);

CREATE INDEX ix_sessions_provider ON checkout_sessions(provider, provider_session_id);
CREATE INDEX ix_sessions_status   ON checkout_sessions(project_id, status, updated_at);
CREATE INDEX ix_sessions_cart     ON checkout_sessions(cart_id);
