-- 005 — market capitalization (company valuation). Stored in BILLIONS of
-- the instrument's currency (raw marketCap / 1e9) so the screener slider,
-- filter, and display all share one unit. Sourced from the same
-- quoteSummary call that already provides P/E + payout; NULL until warmed.
ALTER TABLE instrument ADD COLUMN last_mcap REAL;
