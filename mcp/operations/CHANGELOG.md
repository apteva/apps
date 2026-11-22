# Operations release notes

## 0.4.1

- Fix `metrics` hanging, and the app with it, when a contractor had
  commission lines but no job invoiced in the period (for example a job
  closed in the last second of the window). `by_contractor` ran a query while
  another was still open, and the app's single database connection waited
  for itself. No tool or schema changes.

## 0.4.0

Commission settlement: jobs done by a contractor can now be settled in one of
two ways, side by side in the same project.

- **resale** (the default, and how every job worked before): the brand
  invoices the client and pays the contractor. Unchanged.
- **commission**: the contractor invoices the client directly and the brand
  invoices the contractor a commission (a percentage of the net or gross
  amount, a fixed fee per job, or both), per job or monthly.

What changed:

- Resources take `settlement` terms and `settlement_by_brand` overrides
  (`resources_upsert`). When a contractor is assigned to a job (at intake with
  `partner_resource_id`, with the new `work_set_partner`, or by booking, from
  the lead resource), its terms are copied onto the job. Later changes to the
  resource never touch jobs already assigned, and a done job keeps its
  contractor.
- Rate items can belong to one resource (`config_upsert` rate_item
  `resource_id`): that contractor's own price and cost for the code, used when
  it does the job. `price_preview` takes `resource_id`.
- Pricing checks by mode: `min_margin_pct` applies to resale jobs only;
  commission jobs return `expected_commission_cents` and are flagged
  `below_min_commission` under the new `min_commission_cents` policy.
  `settlement_mode` and `margin_check` are returned with every price.
- Completing a commission job accrues its commission line. `work_invoice` on
  a commission job records the contractor's own invoice (`external_ref`,
  `amount_net_cents`, `amount_gross_cents`, `issued_by`) and recomputes the
  commission on it; recording again corrects it. Lines already invoiced are
  never edited: the difference goes on an adjustment line (negative when the
  amount falls or the job is cancelled). Cancelling voids open lines.
- `work_mark_paid` on a commission job records that the client paid the
  contractor. The new `commission_autoclose_days` policy closes commission
  jobs that many days after the contractor's invoice (hourly worker).
- New tools: `commissions_list` (lines, totals, what each contractor owes),
  `commissions_invoice` (one Billing invoice per contractor, brand and
  currency, to the `billing_email` in their terms, or recorded with
  `external_ref`; lines are claimed first so they are never billed twice),
  `commissions_mark_paid`, `commissions_void`.
- `metrics`: `invoiced_cents` and `margin_pct` now cover resale jobs only
  (unchanged for projects without commission jobs). New `gmv_cents`,
  `revenue_cents`, `commissions`, `receivables`, `by_mode`, `by_contractor`;
  `by_brand` adds GMV, commission and revenue.
- `work_get` returns the settlement, contractor, contractor invoice and
  commission lines; `work_search` filters by `settlement_mode` and
  `partner_resource_id`.
- Panel: a Commissions tab, a settlement section and contractor-invoice
  dialog on each job, settlement terms in the resource dialog, and revenue,
  commission and contractor figures on the overview.
- Fix: timestamps written by the app now follow the app clock, which the
  tests pin (a lifecycle test failed once the real date moved away from it).
- Migration 003 adds the settlement columns and the `commission_lines`
  table and rebuilds `rate_items` with a `resource_id` column (ids kept).
  Existing jobs and resources are resale.
