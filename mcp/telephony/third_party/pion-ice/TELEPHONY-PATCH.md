# Telephony ICE restart compatibility patch

Base: unmodified Pion ICE **v4.4.4**, module checksum
`h1:nPhqLkf8NoE9O8NBKd3Xo2NNc2PlBMdM07tWZ+IH/i8=`.
Original MIT license and copyright notices are retained. This directory is a
local module replacement used only by Telephony. It is included in the app's
source package; no global Go cache, other app, native library or server is patched.

Upstream v4.4.4 `Agent.Restart` unsets the selected pair and closes every candidate
socket immediately. Pion ICE v4.4.8 was also inspected; its restart still closes
the old candidates. A dependency version bump alone does not provide continuity.

## Changes

- `restart_media.go`: keep one previous candidate generation separate from the
  new gathering/checklist, including its nominated pair, receive sockets, remote
  caches and original consent credentials. Never add that pair to the new
  nomination checklist.
- `transport.go`: while no new pair is selected, application writes use the
  previously nominated pair. Normal connected writes do not allocate or start a
  worker. DTLS, SRTP, RTP sequence numbers, tracks and encoders are unchanged.
- `agent.go`: preserve the previous sockets and mux registration during restart;
  recognize only the nominated previous local/remote endpoint for fallback;
  respond to its old consent requests only with the original username and valid
  message integrity. New-generation checks retain upstream validation.
- Once a replacement pair is nominated, outgoing data uses it immediately. Keep
  the old receive sockets for 250 ms for in-flight packets, then close the old
  sockets and remove their mux registration. No audio is buffered or replayed.
- Repeated restart attempts retain at most one previous generation. Failure and
  close clean up current and previous generations, and timer callbacks verify
  their generation before cleanup. Existing ICE failure/setup deadlines remain;
  this patch does not keep a dead call alive indefinitely or guarantee continuity
  after genuine network loss or failed reconnection.

No per-packet logging, credential service, database access, second media pipeline
or codec is introduced. The old-path fallback is an atomic pointer read only when
the normal selected pair is absent. A successful restart adds one short cleanup
timer; it does not add a steady-state per-call worker.

## Maintenance and tests

Compare these three modified upstream files and the two new `restart_media*`
files when updating Pion. Remove this replacement once an upstream implementation
provides equivalent, verified restart continuity. Do not silently substitute an
unpatched release in packaging.

```sh
# From mcp/telephony/third_party/pion-ice:
GOWORK=off go test ./...
GOWORK=off go test -race -run '^TestRestart(Media|Keeps)' -count=1 .
GOWORK=off go test -run '^$' -bench '^BenchmarkUDPConnWriteRead$' -benchmem .
```

The new tests keep bidirectional packets flowing while signaling is withheld,
repeat a pending restart, verify nomination and socket cleanup, reject forged
consent and unknown sources, and verify cleanup on close. Telephony's integration
tests also use an independent real Chromium browser through TURN/UDP and TURN/TCP
with a delayed answer; both sides being patched Pion is not sufficient evidence.
