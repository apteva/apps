# Telephony 0.7.3

Fix Telnyx AI handoff after unanswered human offers, preserving all 0.7.2
changes. This is a source release; no staging or production installation is updated.

- Prepare the AI session once, answer an unanswered carrier leg, wait for carrier
  confirmation, then start streaming. Already answered IVR legs skip duplicate answers.
- Track confirmed carrier answer separately from local call status. Migration 035
  backfills confirmation only from provider events or connected-media evidence.
- Persist activation phases, retry counts and deadlines. Allow three command
  attempts per phase, ten seconds for accepted-command confirmation, and thirty
  seconds total, capped by the call deadline. Restart does not reset the budget.
- Mark routing delivery successful only after media connects. Keep caller
  cancellation and late callbacks from reviving a terminated call.
- On activation failure, terminate with `ai_activation_failed` and preserve one
  callback opportunity. If an answer callback is lost and rejection fails, try
  hanging up the same leg. Carrier termination errors remain visible and retryable.
- Record AI talk time from media connection, including explicit zero talk time
  for terminal AI calls without media. Add administrative activation diagnostics.
- Preserve human adviser routing and browser audio behavior. Recovery queries
  use an index restricted to active inbound Telnyx AI calls.

Validation: full Go unit and local sidecar integration/browser suites, targeted
race tests, migration/restart and cancellation regressions, and a compiled-sidecar
human-offer expiry → AI handoff with signed callbacks and two-way audio through
local carrier/Core substitutes. Release checks also cover frontend tests,
typechecking, frontend build and standalone Go build.

No live carrier call has been placed for this release. Local protocol tests do
not prove PSTN behavior, and Telephony cannot force remote termination while the
carrier API is unavailable. No Telnyx integration JSON or SDK/server update is
required. Existing unrelated local work is retained.
