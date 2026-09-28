# Telephony 0.6.9 (local candidate)

This version adds a generic, bounded sequential decision loop to published
inbound flows. A configured Functions app chooses each destination from a pinned
allowlist. Telephony records the decision and offer outcome, reserves capacity,
and asks again after decline, timeout, or failed setup. The loop ends when the
function reports exhaustion, selects fallback, an adviser connects, the caller
leaves, or the configured attempt and total waiting limits are reached.
An offered destination that is disabled or loses its verified Telephony access
is released on the next routing tick. A terminal fallback announcement remains
audible before hangup.

Decision responses now support `wait_retry` and `exhausted`. Function failures
have a separate bounded retry path and `routing_error` classification. Repeated
offers to the same verified adviser are rejected by default. Browser offers can
record a receipt or explicit decline, independently of answer and media
connection. Terminal lifecycle payloads include a classification and stable
callback opportunity ID where appropriate. Existing one-shot decision flows
retain their default behavior.

The flow editor exposes the limits and policies. See
[routing-decisions.md](routing-decisions.md) for the Functions request and response
contract.

Local verification: Go short suite, frontend typecheck and tests, panel build,
and focused loop, offer, migration, and flow validation tests. A live carrier
call and production installation have not been performed.
