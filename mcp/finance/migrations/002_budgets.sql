-- finance v0.2: per-category monthly budgets.
--
-- "Spend at most €X on groceries per month." Spent / remaining is
-- computed live from `transactions` — no cached value, no separate
-- ledger. A budget with category_id NULL is the "total monthly spend"
-- guardrail: it counts every expense-kind transaction in the period.
--
-- Hierarchical rollup: a budget on the Food category counts spending
-- in Food → Groceries and Food → Restaurants too. The category
-- descendant set is resolved at status-query time via a recursive CTE.
--
-- Multi-currency: budget.amount is in budget.currency (always the
-- project's base_currency at insert time). Transactions in other
-- currencies are converted via fx_rates the same way net-worth does.
--
-- "Spent" counts kinds: expense, fee, tax. NOT withdraw (cash leaving
-- ≠ consumption), NOT buy/sell/transfer/valuation.

CREATE TABLE IF NOT EXISTS budgets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    -- NULL = top-level "total spend" budget across all expense
    -- categories. Useful as a guardrail above per-category caps.
    category_id INTEGER REFERENCES categories(id) ON DELETE CASCADE,
    period      TEXT    NOT NULL CHECK(period IN ('weekly','monthly','quarterly','yearly')),
    amount      INTEGER NOT NULL,                        -- minor units, budget.currency
    currency    TEXT    NOT NULL,
    starts_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    archived    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, category_id, period)
);

-- SQLite treats NULL as distinct in UNIQUE, so the constraint above
-- doesn't prevent multiple total-spend (NULL category_id) budgets.
-- Cover that with a partial unique index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_budgets_total_uniq
    ON budgets(project_id, period) WHERE category_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_budgets_project ON budgets(project_id, archived);
