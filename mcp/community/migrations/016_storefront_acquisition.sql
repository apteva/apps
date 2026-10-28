-- Trusted checkout snapshot; no PII or provider secrets in attribution.
CREATE TABLE storefront_checkout_context (
  checkout_session_id INTEGER PRIMARY KEY,
  community_id TEXT NOT NULL,
  catalog_price_id INTEGER NOT NULL,
  subtotal_cents INTEGER NOT NULL CHECK(subtotal_cents > 0),
  discount_cents INTEGER NOT NULL DEFAULT 0 CHECK(discount_cents >= 0 AND discount_cents < subtotal_cents),
  currency TEXT NOT NULL,
  promotion_code TEXT NOT NULL DEFAULT '',
  first_purchase_only INTEGER NOT NULL DEFAULT 0,
  reservation_id TEXT NOT NULL DEFAULT '',
  attribution_json TEXT NOT NULL DEFAULT '{}',
  purchase_id TEXT UNIQUE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX storefront_context_community ON storefront_checkout_context(community_id,purchase_id);
