# Media write, cleanup and lease diagnostics

Local Telephony change, based on `d5c135e5` (carrier stream recovery after
0.11.4). No staging/production changes, installation, real carrier calls or
publication are part of this work.

## Freshness and socket completion

Live PCM retains the existing 120 ms queue-size limit, 250 ms queue-residence
limit and 320 ms source-age limit. Expired source/queued frames are discarded
before a socket write, without a transport error. A valid frame gets a fixed
250 ms socket write deadline, independent of its remaining queue/source budget.
Downstream source-age checks remain enabled. Synchronous model/control traffic keeps its
existing five-second bound, apart from close replies, which use the bounded
close path. Queued advisory controls retain a 250 ms write bound.

A timeout before any bytes are written can discard that frame and continue on
the same connection. A partial WebSocket frame or another transport error still
closes the socket: continuing would corrupt frame boundaries. Entirely unsent
write-timeout loss now has its own `write_timeout_bytes` and
`write_timeout_drops` counters; it is not included in `stale_bytes`. Audio-health
loss totals continue to include both drop categories. Partial-write failures
remain separately recorded as failed bytes and transport errors.

Each queue's server diagnostics exposes bounded `drop_events` and `transport`:

- UTC timestamp and connection ID (browser socket or carrier generation/leg).
- `queue_expired`, `source_expired`, `socket_write_timeout` or
  `socket_write_error` reason.
- PCM byte count; queue residence before writing; source timestamp, sequence,
  mapped source clock and observed source age where available.
- Write duration, deadline, bytes actually written, WebSocket opcode and
  whether local shutdown was already requested.
- Socket write/error/timeout totals and maximum write time.
- Forced-close count, close time and duration.

There are at most 32 recent events per queue/transport snapshot; cumulative
counters survive reconnection merges. Events contain no media payload, URL,
headers or media credentials. They are collected in memory on failures/drops,
with no per-frame database writes or logging. Existing telemetry persistence
carries them into call diagnostics.

## Bounded cleanup and replacement

Graceful cleanup has a 500 ms forced-close timer even if a data write
or control queue is blocked. Peer/protocol close replies have the same
independent bound. Normal close-frame delivery and the existing grace period
are retained when the connection permits them. Physical interruption precedes
telemetry locks; concurrent close paths count one forced close.

Carrier generation journals include `socket_summary` for carrier and Core/human
loopback legs after cleanup. Generation ownership, caller cancellation, stale
callback rejection, same-call recovery budgets and Telnyx's three-attempt restart
contract remain as implemented in `carrier-media-recovery.md`. No app restart,
new dial, rerouting or duplicate answer is introduced.

## Temporary database failures

New media attachments/renewals still fail closed with HTTP 503 on temporary
lookup failures. An established browser socket can retain only its previously
verified lease. Temporary session, policy or owner lookup failures do not extend
it. If the session table remains unavailable when that lease expires, the close
reason is `media_lease_expired`, distinct from revocation or lookup failure.

The browser socket snapshot adds a separate `lease_events` list for transition-only `lease_check_deferred`,
`lease_check_recovered` and `lease_check_failed`, including timestamp, connection
ID, known expiry (Unix seconds) and remaining lease milliseconds. Repeated
identical failed checks do not emit one event every second. Verified revocation
and replaced-token checks remain authoritative.

## Local verification

Regression coverage includes aged-but-valid frames receiving a full write
budget; stale frames producing no socket writes; exact timeout/drop accounting;
partial-write failures; concurrent/forced cleanup; blocked protocol close
replies; bounded snapshot/reconnect evidence; persisted Telnyx generation socket
summaries; live media through a session-table outage; successful recovery; and
expiry without a working session lookup.

Final checks on 9 October 2026:

- Full Go suite: **853 passed tests/subtests, zero failures**. Three existing
  opt-in skips: query performance and two real Twilio account/call checks.
- Broad recovery/lease/socket race suite: passed (90.445 s). Final focused race
  check including source timing, protocol cleanup and lease/connection
  attribution: passed (12.286 s).
- Frontend: **223 passed, zero failures** (100 frontend and 123 audio tests).
  An initial concurrent run had one timer-sensitive coaching renewal assertion;
  the separate fresh full run passed without changing that test or client code.
- TypeScript typecheck, Go vet, Go build and whitespace checks: passed.

The new concurrency test initially assumed a forced-close event must precede
or follow the interrupted write event in one fixed order. Both events can occur
concurrently; its corrected assertion requires one forced-close event without
inventing an ordering guarantee.

Raw local logs: `/private/tmp/telephony-write-lease-verified-go.jsonl`,
`/private/tmp/telephony-write-lease-race.log`,
`/private/tmp/telephony-write-lease-final-race.log`, and
`/private/tmp/telephony-write-lease-frontend-fresh.log`. No live carrier recovery or network performance claim is
made here. Prior browser audio stress benchmark failures documented in
`carrier-media-recovery-verification.md` remain unresolved by these telemetry
changes.
