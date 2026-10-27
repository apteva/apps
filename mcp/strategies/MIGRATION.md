# Moving research out of Trading v0.15

## Ownership

| Domain | App |
|---|---|
| Portfolios, broker accounts, balances, positions, orders, fills, portfolio risk and P&L | Trading |
| Strategy definitions/versions, assignments, scorecards, backtests, validation and strategy attribution | Strategies |

The dependency goes **Strategies → Trading**. A project agent team can use
Trading alone for direct execution and portfolio review.

## Upgrade procedure

1. Back up the existing Trading database using the deployment's normal backup
   mechanism. Upgrade Trading to v0.16. Its schema migration preserves the ledger
   and research rows and pauses legacy strategy assignments. Its workers no
   longer evaluate or execute assignments.
2. Install an empty Strategies app in the same project. Bind it to the existing
   Trading installation. Trading >=0.16 is required.
3. Choose **Import legacy research** in the Strategies panel, or invoke
   `legacy_import`. The import reads a versioned, project-scoped export through
   the authenticated app integration. It does not open Trading's database file
   and does not copy broker credentials or broker bindings.
4. Verify saved strategy versions, scorecards and research history. Strategy IDs,
   version numbers, backtest IDs, captured data and completed results are
   preserved. Imported assignments stay paused; interrupted runs and validation
   suites import paused. Repeating a completed import is a no-op. Import into a
   nonempty research database is rejected rather than overwriting research.
5. Explicitly assign a reviewed strategy again when it should run. Any broker
   promotion/evidence requirements and Trading's live arming/risk controls still
   apply. Direct agent orders continue to use Trading independently.

Trading retains the source rows after import. The migration neither deletes the
old research nor transfers portfolio ownership. Avoid downgrading to a version
that could resume the old scheduler while Strategies is active.

## Reproducibility and limits

Old completed results and captured tapes remain available for inspection. Event
artifacts include an engine source hash. A run captured under a different source
hash cannot be silently resumed or replayed under the new binary; capture a new
run from the preserved inputs with the current engine. This keeps historical
results from being presented as newly reproduced evidence.

A Strategies installation fixes the identity of its Trading source once it has
observed portfolios. Rebinding it to an unrelated Trading installation is
rejected, because portfolio and strategy identifiers could collide. Use a fresh
Strategies installation for a different Trading source.

The original database schema and some private compatibility code stay in Trading
for migration and existing-ledger compatibility. Trading does not advertise the
old research tools or routes and does not run their scheduler.
