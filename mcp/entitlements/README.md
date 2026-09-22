# Entitlements

Shared access-control and usage layer.

## Prepaid credits

Version 0.3 adds a generic append-only credit ledger. `credits_grant` records
grants and compensating refunds with an idempotency key; `credits_reserve`,
`credits_commit`, and `credits_release` provide atomic spend protection. Use
`credits_balance` for available/reserved totals and `credits_transactions` for
the audit trail. This ledger is separate from `usage_record`: gauges and
counters describe consumption, while credits describe a purchased balance.

Entitlements answers: can this subject access this feature/key, and how much usage have they consumed?

Subjects can be customers, users, accounts, projects, courses, communities, or any app-defined identity.

Feature keys are app-defined strings such as:

- `plan:pro`
- `course:123:view`
- `course:123:certificate`
- `academy:all_access`
- `api:requests`
