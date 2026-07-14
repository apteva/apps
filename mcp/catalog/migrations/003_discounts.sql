-- Catalog v0.2.0 -- reusable discounts for one-time and recurring prices.
--
-- Catalog owns eligibility and redemption accounting. Callers snapshot the
-- returned application terms and decide when to apply them (for example, a
-- checkout applies "once" and subscriptions apply "repeating" per cycle).

CREATE TABLE catalog_discounts (
  id                               INTEGER PRIMARY KEY,
  project_id                       TEXT    NOT NULL,
  name                             TEXT    NOT NULL,
  description                      TEXT    NOT NULL DEFAULT '',
  discount_type                    TEXT    NOT NULL,
  percentage_bps                   INTEGER NOT NULL DEFAULT 0,
  value_cents                      INTEGER NOT NULL DEFAULT 0,
  currency                         TEXT    NOT NULL DEFAULT '',
  duration                         TEXT    NOT NULL DEFAULT 'once',
  duration_cycles                  INTEGER NOT NULL DEFAULT 0,
  starts_at                        TEXT    NOT NULL DEFAULT '',
  ends_at                          TEXT    NOT NULL DEFAULT '',
  max_redemptions                  INTEGER NOT NULL DEFAULT 0,
  max_redemptions_per_customer     INTEGER NOT NULL DEFAULT 0,
  minimum_subtotal_cents           INTEGER NOT NULL DEFAULT 0,
  active                           BOOLEAN NOT NULL DEFAULT 1,
  metadata                         TEXT    NOT NULL DEFAULT '{}',
  created_at                       TEXT    NOT NULL,
  updated_at                       TEXT    NOT NULL,
  archived_at                      TEXT    NOT NULL DEFAULT '',
  CHECK (discount_type IN ('percentage', 'amount', 'price_override')),
  CHECK (duration IN ('once', 'repeating', 'forever')),
  CHECK (percentage_bps >= 0 AND percentage_bps <= 10000),
  CHECK (value_cents >= 0),
  CHECK (duration_cycles >= 0),
  CHECK (max_redemptions >= 0),
  CHECK (max_redemptions_per_customer >= 0),
  CHECK (minimum_subtotal_cents >= 0)
);

CREATE INDEX ix_catalog_discounts_project
  ON catalog_discounts(project_id, active, archived_at, starts_at, ends_at);

CREATE TABLE catalog_discount_scopes (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT    NOT NULL,
  discount_id    INTEGER NOT NULL REFERENCES catalog_discounts(id),
  scope_type     TEXT    NOT NULL,
  scope_id       INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT    NOT NULL,
  CHECK (scope_type IN ('all', 'product', 'price')),
  CHECK ((scope_type = 'all' AND scope_id = 0) OR
         (scope_type <> 'all' AND scope_id > 0)),
  UNIQUE(project_id, discount_id, scope_type, scope_id)
);

CREATE INDEX ix_catalog_discount_scopes_lookup
  ON catalog_discount_scopes(project_id, scope_type, scope_id, discount_id);

CREATE TABLE catalog_discount_codes (
  id                 INTEGER PRIMARY KEY,
  project_id         TEXT    NOT NULL,
  discount_id        INTEGER NOT NULL REFERENCES catalog_discounts(id),
  code                TEXT    NOT NULL,
  normalized_code     TEXT    NOT NULL,
  active              BOOLEAN NOT NULL DEFAULT 1,
  max_redemptions     INTEGER NOT NULL DEFAULT 0,
  metadata            TEXT    NOT NULL DEFAULT '{}',
  created_at          TEXT    NOT NULL,
  updated_at          TEXT    NOT NULL,
  archived_at         TEXT    NOT NULL DEFAULT '',
  CHECK (max_redemptions >= 0),
  UNIQUE(project_id, normalized_code)
);

CREATE INDEX ix_catalog_discount_codes_discount
  ON catalog_discount_codes(project_id, discount_id, active, archived_at);

CREATE TABLE catalog_discount_reservations (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  public_id             TEXT    NOT NULL,
  idempotency_key       TEXT    NOT NULL,
  request_fingerprint   TEXT    NOT NULL,
  discount_id           INTEGER NOT NULL REFERENCES catalog_discounts(id),
  code_id               INTEGER REFERENCES catalog_discount_codes(id),
  customer_ref          TEXT    NOT NULL DEFAULT '',
  context_ref           TEXT    NOT NULL DEFAULT '',
  product_id            INTEGER NOT NULL DEFAULT 0,
  price_id              INTEGER NOT NULL DEFAULT 0,
  quantity              INTEGER NOT NULL DEFAULT 1,
  currency              TEXT    NOT NULL,
  subtotal_cents        INTEGER NOT NULL,
  discount_cents        INTEGER NOT NULL,
  total_cents           INTEGER NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'reserved',
  snapshot_json         TEXT    NOT NULL,
  expires_at            TEXT    NOT NULL,
  created_at            TEXT    NOT NULL,
  updated_at            TEXT    NOT NULL,
  redeemed_at           TEXT    NOT NULL DEFAULT '',
  released_at           TEXT    NOT NULL DEFAULT '',
  CHECK (status IN ('reserved', 'redeemed', 'released', 'expired')),
  CHECK (quantity >= 1),
  CHECK (subtotal_cents >= 0 AND discount_cents >= 0 AND total_cents >= 0),
  UNIQUE(project_id, public_id),
  UNIQUE(project_id, idempotency_key)
);

CREATE INDEX ix_catalog_discount_reservations_capacity
  ON catalog_discount_reservations(project_id, discount_id, status, expires_at);

CREATE INDEX ix_catalog_discount_reservations_customer
  ON catalog_discount_reservations(project_id, discount_id, customer_ref, status, expires_at);

CREATE INDEX ix_catalog_discount_reservations_code
  ON catalog_discount_reservations(project_id, code_id, status, expires_at);
