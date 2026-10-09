# Carrier media recovery

Implemented locally after the 9 October Telnyx incident report. This work does
not establish who initiated the original seven transport failures. It does not
install or configure staging/production, or place real calls.

## Ownership and recovery

The JSON, Twilio and binary programmable-media handlers share one lifecycle.
Each accepted socket has a generation ID and a cancelable runtime lease. Healthy
duplicate sockets are rejected. Confirmed transport failure cancels the failed
generation and closes its sockets; a replacement may take ownership before the
old handler's deferred cleanup finishes. Only its generation can release the
claim, change current media state or replace current carrier diagnostics.

Recovery retains the carrier call ID, adviser, browser authorization and human
hub. It does not offer another adviser, answer again, dial, or stop an existing
carrier call. Caller termination cancels handlers and in-flight restart requests;
late media/callbacks cannot revive the call. An actual replacement socket and
valid decoded media, including silent PCM, confirm recovery. A provider's
`streaming.started` notification alone is insufficient.

Protocol pings run every five seconds. Twenty seconds without data or control
receipt is a transport-liveness failure. Speech activity, microphone mute and
hold are not recovery signals. A socket responding to pongs remains live during
silence. Established frame processing uses an atomic check and never takes the
failure-reporting mutex or performs SQL for each frame.

The existing per-call `call_media_recovery_timeout_seconds` setting bounds
recovery (default 120 seconds, allowed 30–600). Replacements inherit the remaining
budget and attempt count. A replacement that never produces valid media cannot
escape that deadline. Exhaustion records `recovery_failed` and hands final
termination to the existing lifecycle watchdog.

## Carrier capabilities

Adapters may implement the optional `carrierStreamRestarter` contract. Telnyx
uses its existing `start_streaming` integration tool with the same call control
ID, inbound track and current L16/16 kHz bidirectional media profile. It makes
at most three requests, with bounded backoff/jitter and five-second request
deadlines capped by the remaining budget. Command IDs are distinct and stable
per attempt. A replacement cancels an outstanding request. The claim lock is
released before the API call so a provider can connect before its API returns.

No Telnyx integration JSON changes are required. Other adapters retain safe
provider-driven replacement and bounded failure handling; no unsupported
restart commands are invented. Twilio status notifications remain advisory
while its actual socket is healthy. Direct SIP media is outside this WebSocket
stream restart contract.

## Failure evidence

Operator call audio-health detail includes `carrier_bridges`. It contains
generation/stream IDs, start and valid-media times, immutable first failure,
cleanup time, deadline, attempts and a bounded event journal. Events distinguish
provider reason/ID/occurrence time, an actually received WebSocket close frame,
raw read/write failures and their leg, local cleanup, command result and
recovery completion. Both authenticated Telnyx webhook paths share the parser
and generation checks. Old stream IDs/timestamps and ambiguous replacement
callbacks cannot fail a recovered generation.

History retains at most 32 generations per call and 48 bounded events per
generation. Background deletion removes terminal/replaced histories older than
30 days in indexed batches. Callback/media secrets and bridge URLs are redacted.
Carrier timing stages carry generation IDs alongside source sequence/timestamps.
First failure remains available even when a normal call-end reconciliation
corrects the current call's closure classification.

## Browser evidence

The existing PCM worker records transit/source/queue timing before late-frame
rejection. The worklet already records timestamped underrun intervals, dropped
bursts and adaptive target/queue/reserve observations. Capture mute omissions
remain distinct from unexpected loss. Stale-frame dropping, DSP, codecs,
adaptive targets and latency limits are unchanged.

Shared PCM/WebRTC runtime telemetry additionally observes tab visibility, page
freeze/resume/hide/show and audio-device changes. PCM device mute/unmute is now
timestamped as it already was for WebRTC. These event listeners use the existing
bounded reporting channel; they do not enumerate devices, ask permissions, add
timers, change media or terminate calls. They are removed on cleanup/reconnect.

## Verification

`carrier_bridge_recovery_test.go` covers authenticated Telnyx failures, actual
local carrier/browser sockets and duplex resumption, replacement before old
cleanup, generation-scoped diagnostic writes, stale/duplicate callbacks, first
failure/actual close preservation, three restart attempts, unsupported adapters,
silent/muted/held protocol liveness, seven concurrent failures, replacement or
hangup during a blocked API call, a live replacement without media, normal-end
reconciliation, and established frames under a held reporting lock.

The local tests prove ownership/protocol behavior against a simulated Telnyx
stream and integration API. A controlled real carrier call and edge traces are
still needed to verify Telnyx's operational restart behavior and investigate the
original incident. Live calls were not authorized for this task.

Network benchmark results, including failures, are retained in the verification
report. They must not be described as proof of universally lossless audio.
