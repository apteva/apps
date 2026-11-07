# Softphone delivery telemetry

The shared client separates a successful call-list read from the embedding app's
render callback. `onCalls` may return a promise; neither its execution nor its
completion blocks receipt acknowledgements or subsequent polling. Synchronous
exceptions and asynchronous rejections become `TelephonyCallbackError`, whose
`cause` is the original thrown value. They never inherit an HTTP status from
that value. Exceptions from diagnostic/error observers are isolated too.

```ts
const watch = client.watchCalls(renderCalls, {
  onFailure(failure) {
    // operation: call_list | callback
    // category: http | network | timeout | canceled | invalid_response | callback
    // optional status, code, requestId, clientRequestId; trigger and fetchMs
    reportLocalDiagnostic(failure);
  },
  onCallbackError(failure) {
    // failure.error.cause is the original embedding-app exception.
    reportRendererFailure(failure.error);
  },
  onDiagnostics(health) {
    updateConnectionIndicator(health);
  },
});
// Legacy onError also receives the typed callback wrapper.
// A normal close cancels silently and suppresses late results/ACKs.
watch.close();
```

HTTP failures preserve available status, a safe server code and request IDs.
Network failure, bounded request timeout (default 10 seconds), cancellation and
invalid JSON/shape have separate categories. The direct transport retains a
successful HTTP status for malformed JSON. The management SDK does not expose
that status for its JSON parse error, so the client does not invent one.
`fetchMs` measures the read/validation, excluding render callback time.

Request correlation uses `X-Request-ID`; direct CORS allows and exposes it.
Safe server IDs take precedence over the generated client ID; both are retained
when available. The server logs IDs and unsuccessful route/status only, without
request bodies or credentials. Raw errors remain local to callbacks. Persisted
snapshots exclude exception messages, stacks, URLs and arbitrary response text.

## Evidence stages

Receipt ACK remains receipt only. The generic API accepts explicit host reports:

```ts
await client.reportOfferEvent({call_id, offer_id, stage: "ui_displayed"});
await client.reportOfferEvent({call_id, offer_id, stage: "ringtone_attempted"});
// Only after playback evidence supplied by the browser:
await client.reportOfferEvent({call_id, offer_id, stage: "ringtone_started"});
```

Additional stages cover display failure/hiding, ringtone failure/stopping/
suppression and answer attempts/failures. The native Calls panel records display
only after the committed card is visible in the viewport. Its WebAudio ringtone
records started after the running audio clock passes a rendered tone; this
confirms browser rendering, not physical audibility through speakers/headsets.
It reports disabled sounds and unavailable/suspended audio separately.
Third-party hosts must instrument their own display/audio operations.

Tab IDs are shared within the browser document; each client has a distinct
session ID, and each watch its own ID. Reload creates a new tab correlation ID.
Watch diagnostics include connection/reconnect/disconnection counts and times,
last heartbeat, last successful list read, consecutive read failures and the
last categorized failure. Stream state and list-read health are independent.

## Retained diagnostics

`POST /softphone/telemetry` accepts bounded events and optional watch snapshots.
`GET /softphone/offer-telemetry/{call_id}` reads authorized call evidence. The
per-call audio-health response includes `offer_delivery`; the inspector's
Pickup tab separates created, acknowledged, displayed, ringtone and answer
stages by browser tab/session. Claimed/expired offers remain server lifecycle
evidence, not fabricated browser events. Missing evidence does not prove an
adviser ignored a call.

Project and full adviser identity come from authenticated context. User reads
are restricted to current call/history access and their own browser evidence;
operators can inspect the project call. Ingestion verifies the offered browser
destination and current access, including delayed reports for completed calls.
Events never change ownership, eligibility, offer expiry or call lifecycle.

Events retain 7 days; watch snapshots retain 24 hours. Reports are idempotent,
limited to 16 events/request, 64 events/offer/session, 512 events/offer and 32
watch snapshots/principal. Read limits and truncation are explicit. Cleanup is
indexed and bounded to 64 expired rows/table on ingestion, with no new worker.
Health persistence is best effort, single-flight and coalesced (normally at most
once/10 seconds, urgent transitions at most once/second), active only during the
30 seconds after an incoming browser offer is observed. Set
`persistDiagnostics: false` to disable it. Telemetry failures never block calls,
open a microphone, or start a network/media test.

Browser occurrence/health timestamps are browser observations, subject to clock
skew; server receipt timestamps provide the common inspector timeline. A retained
snapshot is not proof of present connectivity.
