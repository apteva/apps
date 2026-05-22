-- 006 — seed approximate market caps (in billions) for well-known large/
-- mega caps so the column + market-cap filter are usable immediately on a
-- fresh install, before the background warmer fetches live values.
--
-- These are deliberately ROUGH placeholders — the warmer overwrites each
-- with the real quoteSummary value when it reaches that symbol. The
-- "last_mcap IS NULL" guard means this only fills blanks: it never clobbers
-- a value an existing install has already warmed.
UPDATE instrument SET last_mcap = 3500 WHERE symbol = 'AAPL'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 3300 WHERE symbol = 'MSFT'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 3000 WHERE symbol = 'NVDA'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 2200 WHERE symbol = 'GOOGL' AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 2200 WHERE symbol = 'GOOG'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 2200 WHERE symbol = 'AMZN'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 1400 WHERE symbol = 'META'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 950  WHERE symbol = 'BRK-B' AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 900  WHERE symbol = 'AVGO'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 800  WHERE symbol = 'LLY'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 650  WHERE symbol = 'JPM'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 620  WHERE symbol = 'WMT'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 600  WHERE symbol = 'V'     AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 520  WHERE symbol = 'XOM'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 480  WHERE symbol = 'UNH'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 480  WHERE symbol = 'MA'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 450  WHERE symbol = 'ORCL'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 420  WHERE symbol = 'COST'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 420  WHERE symbol = 'JNJ'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 390  WHERE symbol = 'HD'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 390  WHERE symbol = 'PG'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 380  WHERE symbol = 'ABBV'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 360  WHERE symbol = 'NFLX'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 340  WHERE symbol = 'BAC'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 300  WHERE symbol = 'MRK'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 290  WHERE symbol = 'KO'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 290  WHERE symbol = 'CVX'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 280  WHERE symbol = 'CRM'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 250  WHERE symbol = 'CSCO'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 250  WHERE symbol = 'AMD'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 240  WHERE symbol = 'ADBE'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 230  WHERE symbol = 'PEP'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 230  WHERE symbol = 'IBM'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 220  WHERE symbol = 'ACN'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 220  WHERE symbol = 'LIN'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 210  WHERE symbol = 'TMO'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 210  WHERE symbol = 'MCD'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 210  WHERE symbol = 'ABT'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 200  WHERE symbol = 'GE'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 200  WHERE symbol = 'DIS'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 190  WHERE symbol = 'INTU'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 190  WHERE symbol = 'QCOM'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 190  WHERE symbol = 'CAT'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 190  WHERE symbol = 'NOW'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 180  WHERE symbol = 'PM'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 175  WHERE symbol = 'TXN'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 175  WHERE symbol = 'VZ'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 175  WHERE symbol = 'ISRG'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 170  WHERE symbol = 'GS'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 165  WHERE symbol = 'MS'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 160  WHERE symbol = 'RTX'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 160  WHERE symbol = 'AMGN'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 160  WHERE symbol = 'PFE'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 155  WHERE symbol = 'SPGI'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 150  WHERE symbol = 'T'     AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 140  WHERE symbol = 'HON'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 140  WHERE symbol = 'UNP'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 140  WHERE symbol = 'LOW'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 130  WHERE symbol = 'BA'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 110  WHERE symbol = 'MDT'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 110  WHERE symbol = 'NKE'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 110  WHERE symbol = 'UPS'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 100  WHERE symbol = 'SBUX'  AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 100  WHERE symbol = 'MO'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 95   WHERE symbol = 'SO'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 85   WHERE symbol = 'DUK'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 75   WHERE symbol = 'MMM'   AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 60   WHERE symbol = 'CL'    AND last_mcap IS NULL;
UPDATE instrument SET last_mcap = 50   WHERE symbol = 'O'     AND last_mcap IS NULL;
