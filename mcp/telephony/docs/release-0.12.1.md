# Telephony 0.12.1

Cumulative release retaining 0.12.0 and every earlier Telephony change.

## Correlated carrier transport evidence

- Authenticated carrier responses include `X-Apteva-Call-ID`, including HTTP
  101 and valid-call rejections. Rejected duplicates retain their proxy identity
  even before a bridge generation exists.
- Explicit local cancellation records its trigger before teardown. Peer close
  frames, read/write failures and local requests remain separate. Cleanup errors
  after cancellation remain visible and cannot overwrite the original failure.
  Synthetic 1006 is never represented as a received close frame.
- Validated Telnyx stream callbacks retain original event type, event/stream IDs,
  carrier and receipt timestamps, and signature outcome. Recovery records command
  and attempt IDs, dispatch/results, safe HTTP status/request ID when supplied by
  the integration executor, and cancellation such as `replacement_connected`.
- Carrier arrival timing survives stream replacement; sequence tracking stays
  stream scoped. Recovery episodes deduplicate inherited attempts.

## Live SIP diagnostics and audio-health boundaries

Every second, active SIP dialogs expose signaling/ACK/refresh/BYE evidence,
codec/encryption, received/sent RTP counts, bytes, packet age/gaps, estimated
sequence loss, duplicates/reordering, jitter, SRTP authentication/replay/decryption
failures, decode/source rejection counts, jitter queue/discards/substitution and
send/pacer/forwarding measurements. This includes local IVR before any browser
attaches. Packet age or send gaps alone do not establish a fault: silence, DTX
and hold remain distinct from confirmed errors. SIP refreshes remain distinct
from failed refreshes; a 491 collision is not counted as a carrier failure.

The existing SSE audio-health dashboard/widget distinguishes carrier SIP/RTP
(or carrier stream), browser WebRTC/WebSocket, and internal processing. Operator
call details include bounded transport history, live snapshots, commands and
recovery episodes. Expanded details coalesce SSE changes and never overlap
requests. Ordinary cleanup read/write errors remain evidence without adding a
live carrier-error counter.

## Performance boundaries

No SQL, JSON encoding, log writes or channel waits were added to successful RTP
packet processing. Packet counters use atomics; sequence timing skips calculation
under contention and reports the skipped observations. A skipped timing window
restarts sequence estimation so reporting contention cannot fabricate packet loss. The jitter snapshot uses a
best-effort bounded read of at most 12 buffered packets. Pacer and forwarding
 snapshots also skip busy locks, retain cumulative counters and expose availability. The background collector
has a 2,048-record channel, 2,048 pending records, at most 1,024 registered sources,
and 200-record SQL batches with a one-second context. Graceful unmount drains queued evidence best-effort after transport teardown,
with a one-second budget and no wait for a busy collector. Overflow discards diagnostic
records and logs the count; it does not pause or terminate audio.

Live state updates every second. Healthy history is retained every five seconds;
incidents, transition events and final snapshots are retained immediately when
collected. Per-call retention is 96 samples, 128 incidents and 128 events, expiring
after seven days. This is bounded diagnostic evidence, not a packet capture or a
promise of lossless telemetry during overload.

See [carrier-telemetry-performance.md](carrier-telemetry-performance.md) for local
packet and complete sampling/persistence/dashboard benchmark measurements.

## Proxy dependency

Telephony consumes the observational `X-Apteva-Proxy-Connection-ID` contract and
returns its authenticated call ID. The currently inspected Server implementation
does not yet forward its private WebSocket connection ID, and reads the call-ID
response header only on HTTP 101. Completing cross-proxy correlation on that
Server needs a companion Server change: overwrite/forward its generated ID and
retain the authenticated response ID on rejection too. No Server/SDK/integration
files are changed in this release. Missing proxy metadata never rejects audio.

## Verification and release scope

Regression tests cover EOF/reset/timeout, real peer close and failed close reply,
local cancellation, after-cancel errors, 409/101 identity, project isolation,
replacement arriving before command result, cross-generation gaps and retry
episodes, bounded collector/retention, RTP sequence wrap/reordering/DTX, SRTP
authentication/replay, live SIP without a browser, ACK/refresh/BYE, hold, expected
cleanup and concurrent sampling. The release gate includes full Go and frontend
suites, focused race checks, vet, frontend/panel builds and Linux compilation.

Publishing this release does not install it, restart any instance, enable SIP,
change routes, or place calls. Carrier/network interoperability remains subject
to separately authorized testing.
