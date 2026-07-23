# Taxes

Use `taxes` for estimates and tracking, not as an official filing engine.
Always treat calculated results as estimates until a human/accountant marks
the obligation filed or paid.

Workflow:

1. Create one `tax_profile` per business tax identity, for example
   `ES_AUTONOMO`, `ES_SL`, `FR_SAS`, or `FR_SARL`.
2. Let profile creation generate the current and next year's applicable
   periods. Use `tax_periods_generate` after changing a structure, VAT
   registration, cadence, fiscal year, or EURL tax regime.
3. Estimate a generated `period_id`. Its type, start, end, and known deadline
   are authoritative for the estimate. Confirm any deadline marked as
   requiring confirmation before creating an obligation.
4. Review warnings and add `tax_adjustments_create` rows for tax credits,
   non-deductible expenses, private-use corrections, penalties, or rounding.
5. Create or update obligations from the estimate.
6. Use `tax_payments_create_bill` when the tax authority payment should be
   tracked through the `bills` app.
7. Record a manual payment only after money leaves the business account.
   `tax_payments_link_bill` links a Bills row without recording payment; a
   tax payment is created only when `bills_payment_id` is supplied and
   verified against that bill.

The tax app reads scoped, date-filtered records from `billing` and `bills`
when available. Explicit revenue, expense, tax, profit, and social
contribution inputs override synchronized values rather than being added to
them. Explicit zero values are preserved.
