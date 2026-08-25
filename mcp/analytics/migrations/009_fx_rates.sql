-- Analytics v0.12.0 - project-scoped reference exchange rates.
--
-- Money-aware aggregates read these rates while leaving the immutable source
-- events in their original currencies. Rates are explicit reference data:
-- callers can audit the source and as-of timestamp used for every conversion.

CREATE TABLE fx_rates (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT    NOT NULL,
  base_currency  TEXT    NOT NULL CHECK (length(base_currency) = 3),
  quote_currency TEXT    NOT NULL CHECK (length(quote_currency) = 3),
  as_of          INTEGER NOT NULL,
  rate           REAL    NOT NULL CHECK (rate > 0),
  source         TEXT    NOT NULL DEFAULT 'manual',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE(project_id, base_currency, quote_currency, as_of)
);

CREATE INDEX ix_fx_rates_project_pair_as_of
  ON fx_rates(project_id, base_currency, quote_currency, as_of DESC);
