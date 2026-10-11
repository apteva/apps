# Telephony 0.11.9

Cumulative release retaining 0.11.8 and all earlier Telephony work.
Publication does not install, restart or configure any instance.

## Long-running diagnostic WebSockets

- Removed the five-minute ceiling in both the echo API and Bun probe. Omitted
  or zero duration runs indefinitely. An optional positive duration ends only
  at that requested time; validation prevents duration overflow.
- The one-use token still has a 60-second admission window. Its expiry cannot
  terminate an established socket. Active reports remain readable throughout
  the run, with ten-minute retention starting at completion.
- An authenticated, project-scoped DELETE stops a probe and invalidates pending
  admission. Ctrl-C/SIGTERM stops the client cleanly. Ten-second checkpoints
  save bounded client evidence during long tests. No silent automatic reconnect
  hides a broken long-lived socket.

## Matching the carrier transport and recording closure evidence

- Echo and live carrier streams share the same five-second ping/liveness watcher,
  20-second activity budget, WebSocket upgrader, buffered reader, writer pump,
  protocol validation and bounded close implementation. Removed the echo-only
  rolling socket read deadline. Live carrier behavior is preserved.
- Human echo writes use the actual 250 ms human carrier budget. Optional
  `media_kind: "ai"` uses the actual five-second AI budget. Reports disclose
  effective codec/profile, write/control deadlines, queue/message bounds,
  liveness and close settings, plus the tighter diagnostic-only limits.
- Structured close triggers distinguish operator/client stop, explicit duration,
  process shutdown, protocol inactivity, control-write failure, peer close,
  read/write errors and frame policy rejection. Canonical underlying errors,
  peer/local close codes, last read/write and separate sent/received ping/pong times, source timing, sampled
  gaps and live/final writer statistics remain available without retaining
  tokens, URLs, payloads or arbitrary peer error text.
- Memory remains bounded: two active probes, sixteen reports, 128 server events,
  256 client events and a bounded pending-echo map. No call rows, carrier commands,
  CRM records, routing claims or audio algorithm changes are introduced.

## Verification scope

Local tests exercise the actual Telephony route and real Bun client alongside
an independent echo. Tests cover unlimited/default and explicit durations,
operator cancellation, expired admission during active runs, retention, project
isolation, settings parity, transport errors and actual protocol inactivity.

The probe mirrors Telephony's carrier socket transport and Telnyx-shaped media
traffic. It does not reproduce Telnyx's own network, provider API commands,
real-call decoding/pacing, or external TLS/proxy idle configuration. Real carrier
output uses provider framing; echo returns received JSON unchanged. A local
soak proves the removed timeout on the tested sockets, not indefinite remote
network reliability. No staging/production instance or real carrier call was
changed or contacted.

### Final release gate

- Complete final-source Go suite: **1,051 tests/subtests passed, five opt-in skips**.
- Frontend/audio suites: **241 tests passed**. Shared client hash, worker and
  worklet bytes match 0.11.8; only frontend version metadata changed.
- Diagnostic and carrier-recovery families passed **three repetitions under
  the race detector**, including actual socket replacement and shared liveness.
- Final-source paired L16 and PCMU probes each returned **300/300 frames** on
  both paths. Default indefinite execution, ten-second checkpointing and Ctrl-C
  termination also passed against the actual endpoint and real Bun client.
- A separate real **315-second** paired soak crossed the removed cap: all
  **15,293 Telephony frames** and **15,297 control frames** returned unchanged.
  Both sockets closed normally; reports retained bounded events/checkpoints.
  Measured RTT/scheduler excursions remain documented in the echo guide and
  are not described as zero jitter or remote quality guarantees.
- SDK dependency remains the topology-verified published **v0.99.0**; all Go
  gates use `GOWORK=off` rather than the unrelated local SDK working tree.

One initial complete run failed a pre-existing one-second recovery-cancellation
assertion. Five consecutive focused reruns and both subsequent complete runs
passed without modifying that recovery/cancellation implementation or assertion.
The original failure log is retained; its root cause is not proven.

Evidence: `/private/tmp/telephony-0119-gate-go.jsonl`,
`telephony-0119-frontend.log`, `telephony-0119-race.log`,
`telephony-0119-probe.log`, `telephony-0119-recovery-cancel.log` and
`telephony-0119-soak.log` (all under `/private/tmp`).
