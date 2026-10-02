# Telephony 0.10.0

Cumulative release retaining Telephony 0.9.1 and all earlier routing, protection,
provider, compliance, call-duration and media improvements.

- Add private supervisor coaching to active human browser calls through the
  existing media hub and adviser socket, across supported carrier bridges.
- Supervisor microphone reaches only the pinned adviser browser. It is excluded
  from carrier output, recordings and other listeners. Existing passive listening
  remains receive-only. Headsets prevent acoustic microphone pickup.
- Add explicit coaching policy/scopes, session mode isolation, bounded queues,
  one active talker, two-second heartbeat expiry and talk-spurt audits.
- Stop coaching on release, blur/backgrounding, call hold/end, revocation,
  ownership/session replacement and media disconnection. Late permission results,
  stale generations and queued frames cannot restart or retarget it.
- Add headless SDK coaching methods and accessible hold-to-talk controls to the
  Calls panel. Preserve normal caller/adviser routing, capture and jitter policy.
- Add isolated audio, permission, takeover, cancellation, deadman and queue
  regressions; rebuild packaged client, worker/worklet and panel assets.
- Correct the local audio benchmark's output-clock mapping using Chromium's
  output timestamp rather than a single main-thread wall-clock sample.

See [design](private-coaching.md) and
[local verification](private-coaching-verification-2026-10-02.md).

No installation is activated by publishing this release. Staging, production,
carrier configuration, routes and phone numbers are untouched. No live calls
were placed.
