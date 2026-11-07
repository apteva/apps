# Trading v0.9.1 audit fixes

Changes prepared in `apps-trading-hardening`, based on commit `49141a87`. Before editing, the trading subtree matched release `trading/v0.9.0` (`f2e710aa`). Released as Trading v0.9.1. No production installation was updated and no real brokerage account was exercised as part of validation.

The fixes cover the 30 findings in the 5 September 2026 audit. Some findings are addressed by rejecting or deferring operations whose correctness cannot be established; the larger architectural improvements listed under limitations remain future work. The original audit and its failing reproductions are preserved separately.

## Changes by finding

| Finding | Correction or protective behavior |
|---|---|
| F01 Replay injection | App-only authenticated caller, isolated replay environment and portfolio checks, strict price/time validation, durable sequential replay steps and idempotent retries. |
| F02 Database deadlock | Close and check the asset-removal query before starting transactional updates, including single-connection databases. |
| F03 Broker routing | Persist connection and execution environment. Missing original connections fail closed. Upgrade recovery accepts only unambiguous ownership. Alpaca environment and stream connection checks prevent routing across paper/live connections. |
| F04 Partial fills | Calculate incremental price from cumulative notional less previously recorded notional. Conditional fill claims prevent duplicate cash/position changes. |
| F05 Cancellation | Persist cancellation intent, retain supervision until broker terminal confirmation, apply fills returned by cancellation, retry pending cancels, and reload order state before subsequent polling. |
| F06 Uncovered sells | Validate unreserved long holdings for every sell. Reject unsupported broker short positions during import instead of silently omitting them. |
| F07 Bitstamp acceptance | Acceptance amount is not treated as execution. Only explicit executed amounts or transactions produce fills. |
| F08 Restart accounting | Preserve existing accounting at startup. Legacy backfill inserts missing records without destroying imported-opening/corporate-action accounting. Add forward position history and restart/upgrade regression coverage. |
| F09 Loss limits | Check loss/drawdown before strategy work, before accepting buys, and before simulated execution. Refresh portfolio state under the placement lock. |
| F10 Strategy rotation | Persist and resume rebalances; execute exits first and wait for settlement before replacement buys. |
| F11 Governance | Require evidence for the exact current strategy version and current execution policy. Definition changes reset promotion; stale assignments and suspended strategies cannot execute. |
| F12 Replay evidence | Preserve creation metadata while updating progress. Hash captured market bars, keep validation scope, and reject dataset replacement after execution begins. |
| F13 Pending policy changes | Revalidate pending paper orders and request cancellation of incompatible broker orders. Store strategy provenance; unknown historical strategy versions fail revalidation. |
| F14 Market freshness | Reject regressive marks, avoid carrying unavailable quote fields across sources, value fresh quotes using their current sides, and expire stale quote books. |
| F15 Valuation | Use one SQL valuation over the portfolio's holdings, with cost fallback for missing marks. New risk-increasing buys are blocked when held assets cannot be priced. |
| F16 Order validation | Validate finite quantities/prices, order types and supported time-in-force combinations. Enforce DAY expiry and immediate IOC/FOK completion or cancellation in simulation. |
| F17 Submission retries | Persist idempotency keys and intent hashes with orders. Both UIs retain the same key when retrying a failed response; trusted MCP call identity is used where available. |
| F18 Ambiguous submissions | Store broker identifiers and connection before reconciliation. Keep uncertainty visible; unsupported missing-ID recovery requires reconciliation rather than claiming a failed order. |
| F19 Account snapshots | Apply cash and positions in one transaction only when the economic revision is unchanged. Defer while orders work; reject duplicate ownership of one connection. Failed separate holdings reads prevent partial snapshots. |
| F20 Replay policy parity | Capture immutable execution/risk/universe/instrument policy; enforce exposure, eligibility, venue quantities and execution costs in strategy replay. Keep simulated quantities consistent with recorded evidence. |
| F21 Replay cancellation | Serialize strategy replay, reload durable state, honor request cancellation, and atomically claim each step with its snapshot. Pause/cancel cannot be overwritten by an in-flight commit. |
| F22 Corporate actions | Use recorded effective-date ownership for distributions, defer ambiguous split application, handle cancelled distributions, transact worthless-removal reads/writes, and close old-symbol history on rename. |
| F23 Economic returns | Include distributions/removals in reported realized economics. Debit commission assets appropriately, track cumulative fee watermarks, and apply late commission-only notifications idempotently to balances, accounting and cost totals. |
| F24 Reference history | Preserve observed listing intervals, prevent quote defaults from overwriting authoritative instrument data, and explicitly label reference history as not survivorship-safe. |
| F25 Portfolio switch | Mask stale fetch results immediately when identity changes and ignore late old-portfolio responses. Key native order forms by portfolio and installation context. |
| F26 Unsaved forms | Preserve dirty risk-policy inputs across performance refreshes and surface mutation failures. |
| F27 Repeated work | Value only held symbols rather than the whole mark universe, remove quote-triggered position-fetch storms, and avoid duplicate economic history/journal entries for unchanged broker snapshots. |
| F28 Objective deadlines | Persist observations and freeze the last pre-deadline result; use observed worst drawdown. Reject backdated period baselines and mark missed baseline/deadline evidence unverifiable. |
| F29 Cost totals | Aggregate across the complete filtered result independently of pagination, return totals by currency, require fresh conversion marks, and reject unconverted non-USD funding amounts. |
| F30 Strategy histories | Require aligned closed-bar timestamps across all symbols and reject incomplete/mismatched or excessively stale live history. |

## Validation

All broker execution tests use mocks and temporary databases. Validation logs are retained with the audit. Release checks were repeated against the SDK v0.74.1 pin.

- Full Go suite: **236 top-level tests passed**, **2 opt-in public-provider tests skipped**; **43 added audit/integrity tests**, including all 16 original reproductions.
- Go race detector: full short suite passed with `-race -short -count=1`.
- Spawned-sidecar integration suite passed: mock-provider REST/order/fill/bootstrap coverage.
- Broker parser fuzzing: 20 seconds, 265,715 executions, no failures.
- Go vet and production binary build.
- Both React interfaces pass TypeScript checks; desk and native embedded bundles rebuilt.
- Four Happy DOM/React interaction tests pass: portfolio switching, out-of-order responses, dirty policy drafts, and retry identity.
- Synthetic valuation benchmark: 100 holdings, 100 versus 10,000 market symbols, 1,000 iterations each: approximately **0.090 ms versus 0.101 ms**, both **1,240 bytes / 18 allocations** per valuation on this Apple M1 Pro. This measures the SQL valuation helper, not end-to-end tick latency.
- Git whitespace validation passes.

Commands use `GOWORK=off` so the app is tested against its pinned SDK dependency, not the workspace overlay. Tests cover concurrency, persistence/restart, failed transactions, duplicate notifications, malformed broker responses, policy changes, replay boundaries and UI state transitions.

## Upgrade behavior

Migration `018_audit_integrity.sql` adds durable broker bindings, idempotency, cancellation/reconciliation state, replay claims, rebalance state, economic revisions, position history, commission watermarks, and objective observations/results.

Live portfolios without one unambiguous historical connection are disarmed and require an explicit `portfolio_broker_bind` call with `confirmation: "BIND BROKER ACCOUNT"`; they remain disarmed after binding. The same connection cannot be owned by multiple portfolios. Existing pending strategy orders whose exact historical version cannot be established will fail policy revalidation. Historical cumulative commission watermarks are recovered without charging balances again.

Existing accounting is preserved. The migration does **not** repair already-corrupted historical accounting or fabricate pre-upgrade ownership, missing corporate-action entitlements, or historical objective prices. Review those records against broker statements before relying on historical performance.

## Remaining limits and follow-up work

- No real-broker contract/order tests or full browser end-to-end tests were performed. Public network provider smoke tests remain opt-in. Happy DOM tests exercise React state transitions, not browser rendering or every UI workflow.
- Binding identifies a platform connection and environment, not a separately verified external account ID. Different connections for the same external account, or credentials changed inside an existing connection, need an account-identity verification layer.
- Kraken, Coinbase and Polymarket submissions with a lost response and no broker ID remain explicitly unresolved; automatic broker-side discovery is not implemented for those cases.
- Forward position history is not a complete historical economic-event ledger. Missing pre-upgrade ownership and complex/revised corporate actions still require reconciliation. Short/margin trading is unsupported. Non-USD corporate cash distributions/funding require explicit conversion. Fees without a reliable conversion or represented asset balance are flagged for reconciliation.
- Paper DAY expiry follows the UTC/replay date; IOC/FOK simulation has no partial liquidity model. Backtest venue policy parity does not recreate full historical exchange calendars, liquidity or delisted universes. Reference snapshots remain survivorship-unverified.
- Objective final values use the last recorded observation before the deadline, not an exact historical deadline valuation. A period baseline missed by more than one minute remains unverifiable. Equity-history freshness uses a conservative allowance for equity market closures.
- Replay is serialized in-process, and some tick work/polling remains. The valuation benchmark and race tests do not establish production throughput or the absence of all logical concurrency defects.

These limitations are not a claim that the original bugs still behave unchanged; they describe where protective rejection/deferment replaces unsafe assumptions and where broader engineering is still needed.
