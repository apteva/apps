-- Commerce v0.7.0 - immutable quote and Catalog discount reservation snapshots.

ALTER TABLE commerce_checkout_sessions
  ADD COLUMN discount_reservation_id TEXT NOT NULL DEFAULT '';

ALTER TABLE commerce_checkout_sessions
  ADD COLUMN quote_json TEXT NOT NULL DEFAULT '{}';
