-- Optional FKs from billing's line items into the catalog app.
--
-- The catalog app owns its own SQLite DB; these IDs are foreign by
-- convention, not enforced at the SQL level (cross-app references
-- can't be FOREIGN KEY constrained in SQLite). Nullable so existing
-- free-form line items (no catalog reference) keep working unchanged.
--
-- The line item's `description` and `unit_price_cents` remain the
-- snapshot of what the customer paid for. The IDs below are for
-- analytics ("revenue by product") and forward-compat reporting.

ALTER TABLE invoice_line_items ADD COLUMN price_id   INTEGER;
ALTER TABLE invoice_line_items ADD COLUMN product_id INTEGER;

CREATE INDEX ix_line_items_price   ON invoice_line_items(price_id);
CREATE INDEX ix_line_items_product ON invoice_line_items(product_id);
