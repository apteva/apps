# Telephony 0.7.0

## Reliability fixes

- Terminal routing plans preserve preceding announcements, including on later
  carrier callbacks. Telnyx waits for answer confirmation before speaking and
  for the matching successful speech completion before hanging up. Unrelated
  playback events cannot end the announcement.
- Terminal and Telnyx menu carrier actions are persisted with routing progress.
  Failed commands survive webhook retries and process restarts, with stable
  Telnyx command IDs and at most five attempts per phase. Retry exhaustion is
  recorded as a routing error. Caller cancellation stops pending actions.
- Late routing work uses the adviser answer claim lock and cannot reset a
  claimed or media-connected call. Failed human answer setup releases the
  matching session, owner and capacity reservation.
- Disabling an inbound route stops new admission while authenticated callbacks
  can still settle existing calls on Telnyx, Twilio, Bandwidth and Plivo.
- Normal terminal routing uses its routing outcome, rather than describing
  routing exhaustion as a lifecycle deadline failure.
- Telnyx suppression sends the required explicit `CALL_REJECTED` cause. The
  carrier contract documents SIP 603; actual upstream retry behavior still
  needs controlled carrier-trace verification.
- Call diagnostics retain separate carrier control, leg and session IDs,
  bounded diversion/history routing headers, and command status/success history.
  Request bodies, response bodies and unrelated SIP headers are excluded.
  `telephony_call_get` exposes these diagnostics within the call's project.

## Compatibility and upgrade

Migration 033 adds durable carrier actions and diagnostic fields without
replacing existing calls or history. Existing Telnyx announcements with stored
state are recovered. The SDK dependency is pinned to v0.89.0. Existing routing,
caller filtering, adviser permissions and frontend APIs remain available.

Explicit caller filtering and per-caller burst suppression reject individual
calls before adviser delivery and exclude them from missed-call projection.
Destination-wide bursts across rotating caller IDs remain alert-only. The
public IVR number is never disabled by these controls.

## Verification

Regression coverage includes answer/speech/hangup failures, duplicate and
unrelated callbacks, restart and upgrade recovery, caller cancellation,
adviser answer races, owner/session failure cleanup, disabled-route callbacks,
classification, and project-scoped bounded carrier evidence. Release checks
also cover the complete local tier 1/2 suite, focused Go race tests, frontend
checks and generated assets.

No production instance is updated and no live call is placed by this release.
A controlled staging call with audible fallback and actual SIP/carrier traces
remains necessary to verify network behavior and identify the source of retries.
