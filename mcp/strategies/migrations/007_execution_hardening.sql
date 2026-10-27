-- Trading execution hardening.
--
-- available_cash is broker-reported spendable cash. cash remains total quote
-- balance so locked funds stay in equity while an order is working.
-- outcome separates a Polymarket YES/NO leg from order direction, allowing a
-- SELL to close either leg without encoding the outcome in side.

ALTER TABLE portfolios ADD COLUMN available_cash REAL;

ALTER TABLE orders ADD COLUMN outcome TEXT;

UPDATE orders
   SET outcome = UPPER(side)
 WHERE asset_class = 'polymarket'
   AND side IN ('yes', 'no')
   AND outcome IS NULL;
