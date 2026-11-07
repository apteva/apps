# Telephony 0.12.42

Adds generic `telephony.account.issue` and `telephony.account.recovered` events
for explicit carrier credit/billing restrictions, suspension, API authentication,
rate limiting and service unavailability. Events are scoped to project, carrier
connection and operation, with fixed sanitized messages and stable issue IDs.

Migration 064 persists issue transitions and an outbox. Delivery uses the Apteva
app bus with deduplication identifiers, ordered retries and bounded work. Credit
recovery requires a confirmed subsequent outbound answer; inventory success and
HTTP call acceptance do not incorrectly clear billing issues. No balance polling
or account-wide call blocking is introduced.

Carrier API, final outbound SIP rejection and authenticated call callback paths
are covered. Metadata lookup is bounded and repeated failures reuse known
provider metadata. See `docs/account-events.md` for evidence limits and subscriber
requirements.

Validation: full Go suite, focused race regressions and go vet passed. New tests
cover explicit and negative classifications, project/account isolation,
concurrency, stale evidence, authenticated callbacks, retry ordering, timeouts,
database reopening, atomic persistence, redaction and preserved calling results.
