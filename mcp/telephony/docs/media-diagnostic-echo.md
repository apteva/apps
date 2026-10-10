# Telephony media transport echo

An operator can create a short-lived probe through this Telephony process. It
uses the same `upgradeBuffered`, `readWebSocketData`, writer pump and bounded
close implementation as the carrier bridge. It exchanges unchanged Telnyx-
shaped JSON frames, including source sequence and timestamp fields. It creates
no call, media claim, adviser reservation, realtime session or CRM record, and
makes no carrier API request.

## API

These paths are relative to the installed Telephony app base, for example
`https://<server>/api/apps/telephony/_install/<installation>`.

1. Authenticated operator `POST /media-diagnostics` with JSON
   `{"duration_seconds":60}`. Add the usual authenticated project scope for a
   global installation. Delegated application users cannot create/read probes.
2. The response contains `diagnostic_id`, `echo_url`, `token`, `token_header`,
   `connect_before` and `report_path`. Connect within 60 seconds; the token is
   single use. Supply it as the `X-Telephony-Diagnostic-Token` handshake header,
   **never in a URL**. Ordinary browser WebSocket constructors cannot set this
   header; use the Bun probe or another authorised diagnostic client.
3. Echo URL: `wss://<server>/api/apps/telephony/_install/<installation>/media/diagnostic/echo`.
   Send `connected`, `start`, and continuous 20 ms `media` frames. `stop` ends
   the probe cleanly. Text frames must contain valid JSON, and media payloads
   must be nonempty base64 (maximum decoded payload 4096 bytes).
4. Authenticated operator `GET /media-diagnostics/<diagnostic_id>` returns the
   report, restricted to the creating project. Fetch while running for counters
   and sampled exchanges; fetch after completion for final closure evidence.

This endpoint must never replace a live carrier media URL. It is diagnostic
transport traffic, not call handling.

## Paired probe

From the Telephony source directory, put the temporary token into the
`TELEPHONY_DIAGNOSTIC_TOKEN` environment variable, then run:

```sh
bun benchmarks/media-diagnostic/probe.ts \
  --url wss://<server>/api/apps/telephony/_install/<installation>/media/diagnostic/echo \
  --control-url wss://<server>/<existing-echo-path> \
  --seconds 60 \
  --codec l16-16 \
  --output /private/tmp/telephony-media-diagnostic.json
```

Both sockets run concurrently. The control must echo text unchanged and should
use the same ingress/proxy but bypass Telephony, like the existing independent
probe. Omit `--control-url` for a single-path test. An authenticated control
needs its own authorised client; this runner never forwards the Telephony token
to the control socket. URL credentials, queries and fragments are rejected.

`l16-16` is the default: 640 bytes per 20 ms, matching Telephony's current
Telnyx L16/16 kHz wire profile. `l16-24` and `pcmu-8` are also available.
Synthetic silence exercises continuous transport without recording speech.
No codec conversion, audio rendering or DSP is performed by the echo.

The client records UTC events, monotonic RTT, receive gaps, send timer lateness,
`bufferedAmount`, missing/duplicate echoes and closure. Timer delays never cause
an artificial catch-up flood; the next source timestamp reflects elapsed time.
The process exits unsuccessfully on an unexpected end, socket error, missing
frames, duplicates or altered audio. A high RTT/gap is reported without claiming
that all network variability is an application failure.

## Evidence and limits

Server reports contain nanosecond-formatted UTC receive/send observations,
monotonic media receive/send gap maxima, write duration, source timing, writer
queue/error measurements, and canonical read/write error classes. Actual peer
close codes are separate from the local closing code. EOF, reset, protocol
failure, timeout and local shutdown remain distinguishable. No token, complete
URL, arbitrary error string or audio payload is retained in a report. Periodic
exchanges are sampled once a second; gaps above 40 ms and writes above 20 ms are
recorded too. Eviction counters disclose truncated evidence.

Bounds: two simultaneous streams per Telephony process, sixteen retained
sessions, 128 exchange events per server report, 256 client events, 1–300 second
probe duration, 16 KiB diagnostic message size, 1000 messages/second per stream,
20 second protocol idle budget, five-second pings, and the existing 250 ms human
carrier write budget/500 ms forced close ceiling. Ping/pong counts as protocol
activity; silence samples remain transport traffic. AI carrier writes currently
have a different five-second budget; this probe represents the human-media
socket budget. Endpoint counters and reports use dedicated in-memory locks;
live call locks, workers and database paths are never involved.

Reports are process-local, expire ten minutes after the maximum session window,
and disappear after a process restart. Copy reports before expiry. No raw media
is stored. Tokens are scoped to their creating installation/process by in-memory
ownership; reconnects need another token. Stop/unmount closes diagnostic sockets.

If only Telephony stalls, inspect Telephony and its specific proxy/connection
path. If both stall together, inspect shared ingress/network and the probe
machine's scheduler/load. Neither pattern alone proves a root cause. UTC clocks
must be compared with clock skew in mind; RTT does not require clock sync.
This does not reproduce Telnyx's network, call admission, pacing, decoding,
WebRTC, browser playback or the entire live call pipeline.

## Local verification

```sh
env GOWORK=off go test -run '^TestMediaDiagnostic' -count=1 .
env GOWORK=off go test -race -run '^TestMediaDiagnostic' -count=3 .
env GOWORK=off go test -tags integration -run '^TestMediaDiagnosticBunProbe$' -count=1 -v .
bunx --no-install tsc -p benchmarks/media-diagnostic/tsconfig.json --noEmit
```

The integration test runs the actual endpoint and Bun client beside a separate
bare echo on localhost, for L16/16 kHz and PCMU. It asserts unchanged frame
counts/payloads and normal closures. Additional tests cover receive stalls,
expiry/replay, project isolation, bounded concurrent upgrades/report retention,
EOF, malformed frames, duration timeout and process shutdown.

Before operational use, validate the installation-specific URL and header
forwarding through staging's real ingress beside the independent probe. This
requires a separately authorised installation/test. No staging or production
installation is changed by the local tests.

### Local verification on 10 October 2026

The complete Go suite passed (1,043 tests/subtests, five opt-in skips), as did
241 frontend/audio tests. Final diagnostic tests, including concurrent claims,
pipelined batches, a forced socket-write timeout and token invalidation on
remount, passed three repetitions under the race detector. Typechecks, vet and
build also passed.

The six-second paired localhost probes exercised the server ping interval:

| Format | Telephony echoes | Control echoes | Maximum echo RTT (Telephony/control) |
| --- | --- | --- | --- |
| L16/16 kHz | 300/300 sent | 300/300 sent | 5.152 / 8.029 ms |
| PCMU/8 kHz | 299/299 sent | 299/299 sent | 8.303 / 8.622 ms |

No transmitted payloads changed or disappeared. PCMU emitted 299 frames;
scheduler timing is measured separately and this is not a claim that every
20 ms source slot ran on time. Both PCMU sockets observed a roughly 43 ms
receipt gap. These are local transport measurements, not live audio quality or
remote Telnyx latency guarantees. Staging and production were untouched.
