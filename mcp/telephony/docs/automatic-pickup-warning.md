# Automatic pickup warning

An optional `warning_tone` inside the existing project/route/destination/adviser
pickup policy gates automatic answering. Existing policies remain unchanged:
warning is off when omitted. For a selected adviser, an authenticated operator
can set the complete identity and preserve their existing mode/delay:

```json
{
  "scope": "adviser",
  "identity": {"issuer_app":"auth","issuer_install_id":"18","subject_type":"user","subject_id":"145","organization_id":"1"},
  "policy": {"mode":"automatic","delay_ms":0,"warning_tone":{"enabled":true,"window_ms":1000}}
}
```

The identity above is an example only; identity/project authorization is still
performed by Telephony. The built-in settings expose the same policy control.
Window defaults to 1000 ms, bounded to 750–3000 ms. Pickup delay and warning
window overlap: total intentional wait is their maximum. Answer now can skip
only a remaining countdown after the warning, never the warning itself. Explicit
manual Answer remains available and cancels automatic pickup.

The shared controller validates/reserves the eligible browser offer, obtains
its exact offer ID and remaining lifetime, then uses the prepared browser audio
runtime. A 180 ms, 880 Hz sine tone (10 ms attack/20 ms release) plays through the
selected output at 20% of configured output volume. Only the local destination
is connected: no microphone graph, MediaStream destination, network/media
socket or carrier path is created for the cue. The controller requires confirmed
rendering, a running audio context and completion of the warning window before
calling the existing answer API. Carrier media starts afterward. Playback
confirmation still cannot prove physical headset/speaker audibility.

Selected non-default output requires `AudioContext.setSinkId`; a missing API,
failed/mismatched device, muted output, suspended context, scheduling failure or
unconfirmed playback suppresses automatic answer. Device selection and playback
are bounded. No fallback silently connects on another output. Existing calls,
media and mute settings are untouched; cancellation stops/disconnects only the
cue and idle preparation. Future calls can prepare audio again if a preceding
call consumed it. Manual and outbound calling retain their normal paths.

Offer withdrawal (including a changed offer on the same call), expiry,
unavailability, session loss, explicit cancellation, disposal or another call
abort pending pickup. Exact handled offer IDs are deduplicated. An asynchronous
completion after cancellation cannot answer. The server rechecks ownership,
availability, effective policy and normal capacity at answer time.

## SDK events and host guidance

Use existing `onAutoAnswer(snapshot)` or softphone snapshots. Structured fields
include state `warning`, `code`, `eventCallId`, `offerId`, `warningStartedAt`,
`warningCompletedAt`, `answerStartedAt` and `manualAnswerAvailable`. Codes do not
contain raw browser exceptions. Call/offer correlation supports embedding UI
translation, for example:

| Code | Example French guidance |
| --- | --- |
| `warning_started` | Bip d’avertissement avant la connexion… |
| `warning_completed` | Avertissement terminé, connexion en cours… |
| `warning_playback_blocked` | Activez le son du navigateur ou répondez manuellement. |
| `warning_output_muted` | Augmentez le volume du casque ou répondez manuellement. |
| `warning_output_failed` / `warning_output_unsupported` | Vérifiez le casque sélectionné ou répondez manuellement. |
| `warning_playback_failed` / `warning_playback_timeout` / `warning_playback_unconfirmed` | Le bip n’a pas pu être confirmé. Répondez manuellement. |
| `auto_answer_warning_unsupported` / `warning_runtime_unsupported` | Actualisez le softphone pour activer le bip, ou répondez manuellement. |
| `offer_expired` / `offer_withdrawn` | L’offre d’appel n’est plus disponible. |
| `pickup_canceled` / `adviser_unavailable` / `another_call_started` | Décrochage automatique annulé. |

`warning_started` means the sequence was attempted, not confirmed audibility.
Retained offer telemetry separates `warning_attempted`, `warning_completed`,
`warning_failed`, `auto_answer_canceled` and actual answer events. The Pickup
inspector displays them independently from receipt/display/ringtone events.
Hosts should render the structured events; they must not play an independent
sound to coordinate connection.

Older clients advertise no warning support and are rejected for automatic pickup
when the effective policy requires it. A server-side completion check prevents
an automatic request without confirmed warning from proceeding. These checks do
not restrict a human's explicit manual answer. Alternate `AudioRuntime`
implementations must supply `playAutoAnswerWarning(options, windowMs, signal)`;
the promise must confirm playback and honor cancellation. The controller also
enforces the minimum window. Runtime implementations without it fail closed.

## Repeatable checks

`bun test frontend/tests/auto-answer*.test.ts` covers output-only graph routing,
selected sink, rendering confirmation/window, blocked and failed playback,
expiry/withdrawal/absence/other calls and late completions. Existing frontend
regressions cover manual/outbound/media/mute behavior. `go test -run
'^TestAutoAnswer' .` covers policy bounds, identity isolation and old-client /
missing-completion rejection. Real Chromium fixtures run with `go test -tags
integration -run '^TestAutoAnswerRealBrowserPickup$' -count=1 .`: zero and delayed
legacy pickup, warning before connection, no answer on cancellation/playback
failure, and silent microphone PCM at the caller peer while the cue renders
locally. No carrier or live test call is used.
