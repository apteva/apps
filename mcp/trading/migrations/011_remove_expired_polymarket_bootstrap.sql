-- Remove expired Polymarket slugs that older versions inserted into the demo
-- portfolio. User-created portfolios and any symbol with executable exposure
-- are preserved.

DELETE FROM watchlist
 WHERE symbol IN (
   'POLY:btc-100k-2026',
   'POLY:fed-cut-march',
   'POLY:recession-2026',
   'POLY:trump-approval-50',
   'POLY:openai-ipo-2026',
   'POLY:gpt5-2026'
 )
   AND portfolio_id IN (
     SELECT portfolio_id
       FROM journal
      WHERE kind = 'note'
        AND json_extract(metadata, '$.source') = 'bootstrap'
   )
   AND NOT EXISTS (
     SELECT 1 FROM positions p
      WHERE p.portfolio_id = watchlist.portfolio_id
        AND p.symbol = watchlist.symbol
        AND p.qty != 0
   )
   AND NOT EXISTS (
     SELECT 1 FROM orders o
      WHERE o.portfolio_id = watchlist.portfolio_id
        AND o.symbol = watchlist.symbol
        AND o.status = 'working'
   );

DELETE FROM marks
 WHERE symbol IN (
   'POLY:btc-100k-2026',
   'POLY:fed-cut-march',
   'POLY:recession-2026',
   'POLY:trump-approval-50',
   'POLY:openai-ipo-2026',
   'POLY:gpt5-2026'
 )
   AND NOT EXISTS (SELECT 1 FROM watchlist w WHERE w.symbol = marks.symbol)
   AND NOT EXISTS (SELECT 1 FROM positions p WHERE p.symbol = marks.symbol AND p.qty != 0)
   AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.symbol = marks.symbol AND o.status = 'working')
   AND NOT EXISTS (SELECT 1 FROM alerts a WHERE a.symbol = marks.symbol AND a.status = 'active');
