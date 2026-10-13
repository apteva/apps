# Browser audio preparation

The softphone verifies immutable worklets before placing, answering, attaching
or taking over a call. Microphone permission preflight runs alongside asset
verification. An explicit reconnect prepares assets before acquiring replacement
session credentials, leaving the current audio running if preparation fails.
Device/context initialization and the new media handshake still happen when the
replacement starts; this is not seamless migration of a live connection.

Same-origin verification is shared per AppHandle and installation/project scope.
Each handle retains at most three successful script URLs; script hashes are
calculated once for the immutable source in the verified client module. Failed
or timed-out requests are evicted. Requests use an eight-second timeout, abort
signal, exact byte comparison and credential-free content-addressed native URLs.
Call and media authorization remain independent of the script cache. A new
frontend module has its own caches and source hashes.

WebRTC verifies only the capture worklet. Automatic WebRTC fallback loads the
PCM worker when needed. PCM listening/coaching and WebRTC coaching share the
same verification cache. External hosts use embedded verified source and keep
individual ownership and cleanup of their blob URLs; preparation introduces no
gateway fetch or microphone access for them.

Sparse UTC setup events record preparation, microphone access, worklet/context
initialization and transport readiness, with monotonic elapsed durations. Events
contain no device IDs, URLs, tokens or arbitrary error strings. Preparation
events are queued before the acquired audio session starts; stages after
session creation use the existing bounded background telemetry sender. Host
observer exceptions cannot fail setup. There is no additional per-frame work.

## Reproducing the preparation comparison

Run from `mcp/telephony`:

```sh
bun benchmarks/softphone/asset-preparation.ts telephony/v0.12.17
```

The script loads the exact released loader alongside the working loader and uses
25 ms simulated HTTP with four responses at a time. It does not measure native
browser module loading, microphone permission, live-call CPU or production.

| Preparation scenario | Verification requests before | After |
| --- | ---: | ---: |
| Cold PCM | 2 | 2 |
| Warm PCM | 2 | 0 |
| 64 simultaneous PCM preparations on one handle | 128 | 2 |
| Cold WebRTC | 2 | 1 |

These are reductions in repeated initialization work, not a guarantee against
network interruptions, device failures or browser scheduling pauses.
