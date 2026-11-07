# Telephony 0.12.48

Managed Telephony now reports admission separately from process liveness. GET
/health returns HTTP 503 while fenced, expired, draining or failed, instead of
claiming readiness. Passive startup remains probeable with an explicit standby
state and admitting:false so replacement does not deadlock before activation.

The protected generic runtime status endpoint reports deployment identity,
admission, ownership expiry, and retained fencing/error timestamps. A fenced
execution that re-verifies its ownership requests a controlled replacement; it
never resumes itself. This enables automatic recovery in Apteva Server builds
supporting the generic status recovery contract. Older controllers still show
accurate admission status but require a managed restart.

Validation covers readiness, protected status, stale ownership, lease expiry,
recovery requests, and coexistence with SDK health routing. Server companion
tests cover automatic replacement, identity verification, confirmed shutdown,
concurrent lifecycle changes and persisted exponential backoff. No carrier,
audio buffering, codec, routing or browser media behavior changes.
