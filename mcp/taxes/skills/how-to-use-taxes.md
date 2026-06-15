# Taxes

Use `taxes` for estimates and tracking, not as an official filing engine.
Always treat calculated results as estimates until a human/accountant marks
the obligation filed or paid.

Workflow:

1. Create one `tax_profile` per business tax identity, for example
   `ES_AUTONOMO`, `ES_SL`, `FR_SAS`, or `FR_SARL`.
2. Open periods for VAT/IVA/TVA, income tax, corporate tax, and social
   contributions.
3. Run `tax_estimate_all` for an overview, or a specific estimate tool for
   one tax type.
4. Review warnings and add `tax_adjustments_create` rows for tax credits,
   non-deductible expenses, private-use corrections, penalties, or rounding.
5. Create or update obligations from the estimate.
6. Use `tax_payments_create_bill` when the tax authority payment should be
   tracked through the `bills` app.
7. Record or link payments after money leaves the business account.

The tax app reads from `billing` and `bills` when available, but callers can
also pass explicit revenue, expense, output-tax, input-tax, profit, and social
contribution amounts for estimation.

