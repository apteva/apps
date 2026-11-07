# SIP clock estimation and bounded recovery

## Scope

The shared server SIP/RTP bridge is used by inbound and outbound calls,
independently of carrier and browser transport. This change corrects its input
clock model; it does not change PCM/WebSocket browser audio, Opus tuning, SIP
negotiation, routing, recording, authorization or call termination.

The 0.12.9 and 0.12.10 SIP buffer sources have the same SHA-256:
`a804f916dd0f13ec7a07cfd55e1838d5d6ff3c69e08c33311fadcc506acb57a7`.

## Defect and correction

The released ingress repeatedly restarted its drift observation interval when
arrival jitter exceeded 8 ms. It rejected late packets before observing their
timing. Frequency corrections also retained the accumulated phase error.

The replacement takes one fastest observation per two-second window, with a
fixed 32-window ring. It fits the relative carrier/server rate over that lower
envelope, even when ordinary jitter continues. In-order timing is observed
before admission, including rejected packets. Duplicates and reordered packets
cannot change ownership of the clock observations.

Frequency and phase are corrected separately and gradually. Rate fits outside
±1,000 ppm or with more than 40 ms residual spread are rejected. A bounded
lower-envelope/recent fit can recover the rate after a timing step, using at
least six observation windows. A phase displacement above 40 ms withholds
phase correction without freezing a valid rate. Accepted frequency steps are
at most 100 ppm per update and preserve current mapped-time continuity; phase
steps are at most 2 ms. A genuine SSRC or backward timestamp change starts a fresh
clock; ordinary DTX and delivery stalls do not erase the rate history. Explicit
hold/resume retains its existing generation reset.

The existing 160 ms default capacity and 200 ms age limit remain. Sustained
400 ms and ten-second delivery delays are rejected, including after the entire
estimator history contains delayed packets. This is a relative RTP clock:
without a synchronized source send clock, it cannot distinguish every gradual
network-delay change from oscillator drift.

The decoded reserve is also reconciled with concurrent admission when a frame
cannot be completed. Previously a tentative consumption could release capacity,
then restore a retained partial above the shared cap. Reconciliation drops oldest
queued audio using the existing overflow accounting.

## Diagnostics and cost

Per-call `/audio-health` SIP snapshots and summary metrics expose estimator
updates, rejected fits, observation-window count and signed accumulated phase
adjustment, alongside the existing rate, ages and drop reasons.

The locally prepared 0.12.14 additionally retains sixteen clock-fit records,
with UTC timestamp, rejection reason, candidate rate, residual spread, phase,
fit method and applied corrections. Rate rejection and phase withholding have
separate counters. A sampling interval can contain both, so these validation
counters should not be treated as disjoint incident counts.

Drop events distinguish total relative source age, relative delivery age at
arrival, local queue age and consumer interval. The age fields overlap; do not
add their maxima together. Decoded partials retain their timing components.
Linux SO_TIMESTAMPNS supplies kernel-arrival times where supported, and socket
residence is reported separately. Unsupported/invalid/future timestamps use
ordinary read time. Converted arrivals retain the local monotonic clock.
Timestamp activation is optional and never waits during call handling.

G.722 reserve correction now has the same 125 microsecond maximum per frame
as G.711: two decoded samples at 16 kHz versus one at 8 kHz. G.722's RTP timestamp
clock remains 8 kHz. Queue capacity and stale-audio limits are unchanged.

Fresh, in-order data received after concealment can trigger a rebuffer when
the queue is empty and only synthetic playback advanced past its sequence.
The source clock is retained; duplicate and reordered data are still rejected,
and age admission always runs first. Retained synthetic partials are cleared
for this playback epoch; the carrier call and source ownership are unchanged.

Startup uses a reserve that fits the oldest packet's remaining 200 ms budget.
With ordinary timing it retains the configured reserve and the existing short
utterance fallback. At the age-budget edge, one fresh 20 ms packet can start
immediately rather than waiting until it expires. Every start exposes the
actual `required_reserve_ms`, alongside the adaptive target and queue.

Live reserve adaptation also respects that budget. Its effective target leaves
one 20 ms output frame of headroom after the latest relative delivery offset,
instead of building the nominal reserve on top of a nearly exhausted budget.
`effective_target_ms` exposes that constrained target. Packet/partial expiry is
unchanged: near-200 ms delivery plus jitter can still legitimately expire before
the next playout tick, and no lossless guarantee is made for that condition.

After samples are consumed from a decoded segment, its remaining source time
advances by their audio duration. Physical arrival time stays fixed, and the
earliest of the two still controls expiry. This prevents a consumed prefix
from expiring fresh remaining audio without legitimizing old socket backlog.

Observations use fixed storage and allocate nothing. Each fit touches at most
32 points twice; a bounded set of fallback fits runs at most once per window.
No database, logging, network request,
timer or goroutine was added to packet admission. Snapshots retain the existing
`TryLock` and background persistence. Reserve reconciliation uses the existing
buffer mutex and bounded queue removal.

A healthy-input ingress/queue microbenchmark (three repetitions) measured a
median 0.424 µs per packet before and 0.443 µs after. Both allocate the same
160-byte payload copy once per packet; the estimator adds no packet allocation.
This comparison uses healthy input on both sides, since benchmarking the broken
implementation while it rejects audio would compare different amounts of work.

## Deterministic verification

The measurements in this section and the original paced-load section below
record the earlier clock fix. See **0.12.14 follow-up verification** for the
current frozen-0.12.13 comparison and final mixed-load results; earlier passing
runs do not supersede the retained failures in that broader test.

`TestSIPPlaybackClockReportedReproduction` reproduces the report's exact input:
20 minutes, 20 ms ordered packets, 250 ppm period difference, alternating 10 ms
arrival variation, no network or CPU pressure. A frozen released ingress rejects
**220 seconds**; the corrected ingress rejects **zero**.

The normal Go suite also runs:

- 24 thirty-minute/two-hour replays: PCMU, PCMA and G.722, ±250 ppm,
  alternating jitter and 0–40 ms jitter. Zero discarded and replacement audio;
  maximum observed queued reserve 99 ms.
- One-hour slowly varying 0–35 ms jitter, both clock directions.
- 32 concurrent twenty-minute G.722 replays.
- Timestamp/sequence wrap, hold/DTX/source replacement and stale partials.
- Sustained delayed delivery and subsequent fresh-source recovery.
- Concurrent ingress, playout and diagnostic snapshots under the race detector.
- Bit-for-bit healthy PCM against the codec reference decode.

### 64 calls, twenty minutes each

Interleaved deterministic buffers and PCMU framers, including a shared process
pause every twenty seconds. Comparison freezes the released **ingress** and uses
the same framer on both sides; it is not a replay of every packet of a real call.
Values below are the worst call's cumulative discarded duration, not a single
continuous outage or a measurement of spoken words lost.

| Input | Released ingress | Corrected ingress |
| --- | ---: | ---: |
| Stable timing | 0 s | 0 s |
| Clock difference + modest jitter | 456.715 s | 0 s |
| Same, shared 80 ms process pauses | 471.258 s | 1.180 s |
| Same, shared 400 ms process pauses | 485.161 s | 23.976 s |

In the corrected 80 ms pause case, all loss is capacity overflow, with no
late/expired rejection or replacement audio. The 400 ms case correctly loses
stale/overflowing audio. Both recover with **zero continued drops outside the
two-second recovery window**. Queue depth remains within 160 ms. No buffer can
guarantee continuous audio if its process stops executing longer than its reserve.

## Paced load verification

An opt-in test uses real local UDP/SRTP in both directions, production G.722
encoding/decoding, framing, resampling and portable/native Opus encode/decode.
The input is synthetic voiced audio, returned through the codec loopback;
microphone hardware and browser playback are not part of this measurement.
Each case runs 16 seconds at 50 packets/second per stream and checks packet
counts, discards, replacement audio, queue capacity and work p99.

A fresh Darwin arm64 run, Go limited to four execution threads, passed at 32 and
64 streams for both codec variants: zero discards/replacement audio and all
expected incoming packets received. At 64 streams with native FEC, CPU was
2.054 cores, playback work p99 0.766 ms, maximum 3.374 ms, and maximum queued
reserve 80 ms. This measures the tested audio stages, **not** a production
capacity limit; signaling, database/routing, recording, proxy and actual browser
rendering are excluded.

An earlier two-thread run on an overloaded computer failed three of four cases;
those reports remain available. Changing the execution-thread limit and host
conditions means the later run cannot attribute the earlier failure to one cause.
Real scheduling stalls still cause legitimate bounded loss.

The full local Go run passed 1,277 tests/subtests with nine optional skips. The
focused SIP/recovery race checks, vet, typecheck, panel and CGO-disabled builds
also passed. All 251 frontend/worklet tests passed in a fresh run without the large replay matrix.
An earlier combined run missed one existing coaching test's 3.7-second retry
deadline while the large virtual replays were running; that failure is retained
with the subsequent passing run, rather than counted as a clean first run.

## Reproduce locally

```sh
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -run 'TestSIPPlayback' \
  -skip 'TestSIPPlayback(LongClockAndJitter|LongClockMatrix|ClockConcurrentLongCalls|ClockMultiCallComparison|ReleasedClockRegression|ClockSlowChangingJitter|ClockReportedReproduction)$' .
GOWORK=off GOMAXPROCS=4 \
  TELEPHONY_SIP_LOAD_RESULTS=/absolute/path/paced-load.json \
  go test -run '^TestSIPPlaybackClockPacedLoad$' -count=1 -v .
GOWORK=off TELEPHONY_SIP_MULTI_RESULTS=/absolute/path/comparison.json \
  go test -run '^TestSIPPlaybackClockMultiCallComparison$' -count=1 -v .
GOWORK=off go test -run '^$' -bench '^BenchmarkSIPPlaybackClockIngress$' -benchmem .
```

The long deterministic replays always run in the full suite. CI avoids repeating
the long serial/large replay matrices under race instrumentation; the focused
concurrent diagnostic/ingress/consumer test exercises the shared clock and
reserve under the race detector.

## Production attribution

Read-only production inspection supports correcting this code weakness: the
worst completed call received 147,523 RTP packets with no sequence loss or SIP
read/decode/send errors, but accumulated 365.600 seconds of SIP buffer discards
and 66 clock resets. Browser loss was separate: 30 packets and 628 ms concealment.
Other inspected calls also had independent browser losses or reconnects.

Host CPU over the 55-minute incident window averaged 27.44%, peaked briefly at
100%, and exceeded 95% for 0.326% of observed time. The sampled call interval
peaked at ten simultaneous calls. Brief scheduling stalls and network bursts
remain possible triggers; neither their boundary nor the actual carrier clock
offset is proven. The retained events are not a complete packet trace.

No production/staging installation, setting, route, carrier resource or real call
was changed or initiated. Local evidence demonstrates the fixed regression and
bounded recovery, not that every production audio problem or reported noise is
resolved.

## 0.12.14 follow-up verification

The final frozen-0.12.13 clock/ingress comparison uses the current framer on both
sides. The twenty-minute G.722 80 ms offset replay changes from 775.86 seconds of
late rejection to zero total discards. PCMU/PCMA, rate changes and alternating
delivery windows also recover. A 147-second G.722 hypothesis probe with a 160 ms
step and one 420 ms hold changes from 34.99975 seconds discarded to 401.25 ms,
with zero tail drops after recovery. The report does not provide the actual
packet timing; these are controlled probes, not production attribution.

The final paced mixed recording/database/notification load run passed native
Opus/FEC at 32 streams, but failed portable 32/64 and native 64. Its strict
zero-drop gate was retained. The native 64 case had a 777 ms maximum scheduling
delay; the earlier unmodified-source timing scenarios and earlier capacity
failures remain recorded. The earlier successful non-mixed test above cannot
be substituted for this broader final test or establish a production limit.

Kernel-arrival support builds for Linux, but final Linux runtime verification
is pending on native Linux/CI. No further Docker test or remote change is part
of this work. [Verification evidence](verification/sip-recovery-0.12.14.json)
contains the final results and limitations.
