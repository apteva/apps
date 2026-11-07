# Browser network resilience — local verification, 25 October 2026

Telephony and its shared softphone only, based on released 0.12.40 (`fb2ca449`).
No production/staging changes, live carrier calls, infrastructure changes,
instance restarts, Docker, release or registry publication.

**Historical verification record. This work is included in Telephony 0.12.41 after subsequent release authorization. The optional guarded upload controller remains experimental and off by default. Earlier adverse runs remain part of the evidence.**

## Delivered

- Project/adviser network profiles keyed by the full authenticated identity
  tuple. Operators control adviser self-editing. Settings apply to the next
  media attachment; saving never restarts an established call.
- Balanced defaults preserve the released transport preference, 32 kbps initial
  and maximum Opus, 16 kbps minimum and requested 60/160 ms playback reserve.
  The unstable-connection preset requests WebRTC with setup fallback, caps Opus
  at 20 kbps, and requests an 80 ms playback reserve. All values remain bounded
  and adjustable. Lower bitrate trades encoding quality for bandwidth headroom;
  it cannot make an inadequate network reliable.
- The profile reaches the browser encoder, server encoder and listener/coach
  sessions. Existing FEC/PLC, native browser congestion control, bitrate response
  to loss/bandwidth, SIP timing, recording and access checks are preserved.
  Unconfigured hosts use the unchanged released controller; a separate adapter
  applies only explicitly saved profiles. Balanced bitrate behavior is tested against a frozen copy of the actual
  released TypeScript controller, including varying bandwidth/loss/RTT feedback.
- Optional quality notices are off by default and remain local, adding no
  active-call event traffic. A sustained-impairment notice uses existing stats, requires real loss/repair
  evidence and is rate-limited. Missing stats, silence, a held/inactive call or
  a browser scheduling pause alone cannot justify a path change. The notice
  does not renegotiate ICE, replace a peer, restart a microphone or hang up.
- Idle manual checks and direct/relay comparisons use an unordered unreliable
  datachannel: 96-byte application packets every 20 ms for six seconds in each
  direction. No microphone permission, audio device, carrier call, recording or
  call ownership is needed. Results contain sampled loss, probe RTT, arrival
  variation and local browser scheduling pauses, without addresses/credentials.
- Results distinguish ready, degraded, unavailable, inconclusive and cancelled.
  Insufficient evidence or a browser pause over 100 ms never becomes ready.
  Tests cannot bypass forced-relay policy. Comparing paths recommends a measured
  route for future calls; existing TURN controls select UDP/TCP/TLS and ports.
- Periodic idle probes are **off by default**. The bundled panel supports them;
  enable `background_check` on the project/adviser profile to use them. External
  hosts opt in with `createSoftphone({networkCheck:true})`. Manual
  `phone.checkNetwork()` is available separately.
- A quick check normally takes about 7–15 seconds; each path has an 18-second
  deadline. It never holds dial/answer busy. Incoming offers, call placement,
  answer, attach and listen/coach cancel diagnostics. Discovery/RPC cancellation
  does not wait for socket cleanup. Runtime drain also closes diagnostics.

Backend bounds: four diagnostic peers, one per identity, one separate credential
RPC slot with a two-second timeout, 25-second TTL and bounded messages/rates.
Call credential slots and the fixed call UDP range are reserved for calls.
The packet probe still consumes bandwidth; one adviser's idleness does not mean
an office WAN is idle. No permanent per-packet worker, log or database write is
added to audio processing.

`applyNetworkPolicy()` applies preferences to the next attachment and restores
host transport on return to inherit. The bundled panel applies adviser saves
immediately. Unsaved defaults never overwrite host preferences, and removing
an override restores the host transport and playback bounds without restarting
live audio. Existing external hosts retain their transport preference unless
they load/apply these optional preferences; server Opus limits apply to WebRTC
regardless.

## Diagnostic networking

Strict NAT/firewall deployments need a separate forwarded diagnostic range:
`softphone_network_check_udp_port_min` / `softphone_network_check_udp_port_max`.
It must not overlap call UDP ports. Otherwise diagnostic sockets use OS ephemeral
ports outside the reserved call range. SIP ports remain unchanged. Failure to
reach diagnostic ports is not proof that call ports are blocked. An unavailable
check never blocks calling. No firewall/DNS/TURN infrastructure is changed.

SCTP/DTLS overhead and behavior differ from RTP. Readiness is a short packet
probe, not a speech-quality guarantee, physical mouth-to-ear measurement or MOS.

## Repeatable evidence

The deterministic 120-second queue model compares the frozen released browser
controller with the actual new controller. Balanced behavior is identical. A
20 kbps cap can create headroom under contention:

| Modeled link | Released: lost packets / p95 queue | New balanced | New 20 kbps preset |
|---|---:|---:|---:|
| Healthy | 0 / 12 ms | 0 / 12 ms | 0 / 10 ms |
| Busy upload, 48 kbps | 60 / 1594 ms | 60 / 1594 ms | 0 / 19 ms |
| Severe contention, 44 kbps | 76 / 1593 ms | 76 / 1593 ms | 9 / 1581 ms |

The severe case remains unacceptable. The model assumes fixed packet sizes,
wire overhead and a finite network queue; native BWE/DTX and speech quality are
not modeled. Do not present these queue values as real phone latency.

Real Chromium 153 through the production bridge: dropping 300 ms bursts plus
additional SRTP packets on a direct path while leaving a local TURN alternative
healthy resulted in 399 receiver-reported lost packets over 15 seconds. Selecting
the healthy relay before calling resulted in zero lost packets. Replacement
samples fell from about 7585 ms to 170 ms, with peers/microphone senders preserved.
This compares paths using the same current code; it is not an old/new binary
handover comparison or proof that all replacement samples contained speech.

The real idle check completed in about 7.4 seconds: 300 packets sent, 299
observed, zero sequence gaps in the observed windows and about 22 ms probe RTT.
Unobserved trailing packets are not classified as proven loss. Microphone
permission remained `prompt`; no calls were created and resources were reaped.
A separate real-datagram impairment test verifies the check detects degradation
rather than returning ready when actual relay packets are dropped.

The actual encrypted-traffic audio benchmark also covers healthy delivery,
64 kbps links, jitter/loss and 120 ms bursts. Each result retains its own gates,
source fingerprint, host observations and adverse results. A 48 kbps link still
reports degraded audio with the lower-bandwidth preset; completing that scenario
does not mean acceptable speech. Detailed final measurements are recorded in
`verification/browser-network-acceptance.json`.

## Experiments excluded from the app

**Automatic direct-to-relay handover failed acceptance.** Chromium retained its
old higher-priority host pair and pruned/waited on the new relay pair. Signaling
success did not mean a media switch. A related gathering-wait modification also
failed repeated TURN renewal and was removed. The released ICE controller and
Pion restart patch are unchanged. No `webrtc.ice_recover` control is introduced.

**Additional custom RTT-based bitrate control failed acceptance.** It improved
a queue model but the real 96 kbps jitter/loss benchmark exceeded its marker-loss
gate: 5.3% over 20 seconds and 5.1% over 60 seconds. The corresponding released
60-second run had 4.2%. These are individual noisy laboratory observations, not
an attribution of every missed marker to that controller; they do not establish
an improvement. The controller and its switch were removed entirely. Existing
native congestion control and released bitrate adaptation remain.

A TURN/UDP integration attempt also lost signaling once after the experimental
ICE change was removed. Two subsequent focused runs of the unchanged renewal
path passed with zero bidirectional packet loss; the released baseline also
passed two runs. That earlier failure is retained as an unresolved observation,
not silently called fixed. Direct and TURN/TCP renewal checks passed.

Do not enable automatic mid-call route switching or additional custom congestion
control until successful AND unsuccessful recovery paths pass real-browser
continuity/quality tests. The delivered diagnostics/settings support controlled
comparisons without those unproven mechanisms.

## Repeat locally

```sh
bun run test:network
bun run test:network:browser
TELEPHONY_NETWORK_RESULTS=/tmp/network-model.json bun test frontend/tests/network-resilience.test.ts
GOWORK=off TELEPHONY_NETWORK_BROWSER_RESULTS=/tmp/network-paths.json go test -tags integration -run '^TestNetworkBrowserDirectVersusRelayRealPacketLoss$' -count=1 -timeout 2m -v .
GOWORK=off go test -race -run '^TestNetwork' -count=1 -timeout 2m .
GOWORK=off go test ./...
bun run test:frontend
bun run typecheck
GOWORK=off go vet ./...
bun run build:frontend
bun ../../scripts/build-panels.ts --app telephony
bun run benchmark:softphone --profiles webrtc-udp-96k-jitter-loss --seconds 60 --seed 20261025 --output /tmp/telephony-balanced
TELEPHONY_BENCHMARK_NETWORK_PROFILE=unstable bun run benchmark:softphone --profiles webrtc-udp-64k,webrtc-udp-48k --seconds 20 --seed 20261025 --output /tmp/telephony-unstable
```

The preset benchmark changes only its local fixture through the real policy API.
CI includes the no-microphone check, impaired-datagram check and direct/relay
packet-loss comparison with existing Chromium/TURN renewal tests. All services
are local substitutes; no live provider account or Docker is needed.

## Remaining audio acceptance gate

The final long conservative candidate still failed the 96 kbps jitter/loss gate:
10.2% of upstream synthetic markers were missed over 60 seconds, versus 3.4% in
a later unmodified-release recheck. A new-client/released-server isolation run
also failed. Preserving the original default controller did not by itself clear
the short stress gate. Host load varied substantially, so these observations do
not establish an exact causal regression. They also do not justify claiming
complete audio-quality non-regression. All failures and source fingerprints are
retained in the acceptance JSON; no thresholds were relaxed.

The functional suite passed 1,973 Go cases/subcases (12 opt-in skips), 340
frontend tests, focused final compatibility/auth/quota checks, race tests,
typechecking, vet, builds, panels, real packet probes and TURN continuity. The
full Go suite preceded the final compatibility-only unsaved-policy guard; its
focused Go/race checks and the full frontend suite passed afterward. No release,
deployment, installation or restart was performed.

### Released-baseline repeatability check

After restoring the separate verification checkout to a clean Git tree (including
byte checks against its baseline commit for the audio client/controller), three
unmodified-release repeats of the 20-second jitter/loss scenario all failed at
7.9% upstream marker loss, matching the preserved-default candidate short run.
This establishes that this harsh gate is not reliably satisfied by the release
either. It does not certify perfect speech or erase the candidate's failed long
run. The default active controller is retained unchanged; optional network
features and their cancellation/authentication tests are green.


## Follow-up: measured resilience and optional guarded upload control

The added `upload_bitrate_control` setting accepts `standard` (default) or
`guarded` (explicit experiment). It is persisted with the existing authenticated
project/adviser policy and applies at the next attachment. The server still uses
its existing RTCP loss-based Opus controller; the new control adjusts the browser
microphone sender's maximum bitrate. Native WebRTC congestion control remains
active underneath that cap.

Guarded control requires an advancing RTCP observation, active outgoing packets,
and bounded report age. It distinguishes cached getStats polls from new feedback.
High, stable RTT and receive-only loss do not lower the upload rate. Delay growth
must coexist with outgoing loss, send-queue delay or constrained available
bandwidth for four seconds. Cuts and recovery are bounded by the configured
16–32 kbps limits; sustained delay cuts by 25%, explicit loss cuts by 4 kbps,
and recovery requires ten seconds of clean fresh observations. Route/SSRC changes
and sampling pauses reset timing evidence. No ICE restart, capture restart,
carrier request, new timer or per-frame policy work is introduced.

The first conservative variant reduced rates too slowly in the independent wire
queue model and was revised. The final model has fixed packet losses and the same
capacity schedule in both runs. It shows fewer overflow losses, but still over a
second of p95 queue delay under the worst modeled contention. This is why the
mode is **experimental**, not the recommended default or a claim of universal
improvement. The standard controller remains unchanged for unconfigured hosts.

Optional quality warnings now cover sustained microphone-upload loss as well as
receive-side loss/concealment. Cached reports cannot create an incident by
repeated polling; path changes reset accumulated evidence. Warnings remain local
and off by default. Diagnostic history records the selected upload-control mode,
its decision reason and feedback freshness within the existing telemetry budget.

### Natural speech acceptance

`TestNetworkSpeechReplay` uses the two checked-in, hash-verified human speech
fixtures, actual production Opus encoding/decoding, RTP jitter admission, the
production packet repair function and the paced release queue. It replays the
same source-index timing/loss faults at 16, 20 and 32 kbps and exercises RTP
sequence wrap. It measures waveform preservation, onset, longest damaged speech,
output gaps, added delay, replacement audio, discards and recovery. Codec changes
caused by packet loss are assessed with strict recovery-tail waveform correlation
and normalized error; healthy paths must be byte-identical to a clean decode.

The six scenarios are healthy delivery, modest jitter, periodic isolated packet
loss (with a clean recovery tail), a 60 ms loss burst, a 420 ms delivery pause and
a constrained 48 kbps link. The constrained comparison uses actual encoded packet
sizes plus 64 modeled wire-overhead bytes, a bounded 200 ms network queue and the
same link schedule. It is an adverse comparison, not a claim that the 32 kbps run
is acceptable. The 20 kbps profile must improve both preservation and delay.

On the local native codec, both constrained speech fixtures improved from
85.1–87.6% preserved active frames at 32 kbps to 100% at 20 kbps, relative to each
rate's clean decode. Local discards fell from 1.60–1.66 seconds to zero; maximum
added delay fell from 158–159 ms to 11 ms. This measures transport preservation,
not whether lower-bitrate encoding sounds identical to 32 kbps. Healthy and
modestly jittered runs remain byte-identical at every tested rate.

`TestMarkerCorruptionIsNotSpeechLossPercentage` separately proves the marker
metric's limitation: removing 0.4% of PCM duration can invalidate 10% of four-tone
markers. Existing browser marker thresholds are unchanged. Prior failures are
retained; this observation does not reclassify them as successful calls.

Repeatable commands (all local, no Docker or carrier account):

```sh
bun run test:network
TELEPHONY_NETWORK_SPEECH_RESULTS=/tmp/network-speech.json bun run test:network:speech
TELEPHONY_GUARDED_MODEL_RESULTS=/tmp/guarded-model.json bun test frontend/tests/rtc-send-policy.test.ts
GOWORK=off go test ./benchmarks/softphone -run TestMarkerCorruption -count=1 -v
TELEPHONY_BENCHMARK_NETWORK_PROFILE=guarded bun run benchmark:softphone --profiles webrtc-udp-96k-jitter-loss --seconds 60 --seed 20261025 --output /tmp/guarded-browser
bun run benchmark:outbound-sip --seconds 60 --calls 8 --output /tmp/outbound-network
```

The speech replay and feedback-controller tests are part of ordinary test runs;
CI also saves their artifacts. Real-browser comparisons, TURN continuity and the
outbound browser→SIP soak remain separate evidence layers. A model/replay pass
cannot replace those checks.

### Boundaries of this work

Telephony provides optional policies, Opus/FEC/PLC, receive buffering, TURN
UDP/TCP/TLS configuration, packet diagnostics, quality warnings and remote media
deployment support. Multiple regional media/TURN servers and router QoS still
require infrastructure configuration. GSM fallback is a separate carrier/mobile
feature. Seamless automatic direct-to-relay switching remains excluded: the prior
real-browser experiment did not prove media continuity. Normal established-path
credential renewal remains supported and tested. Ringover's unpublished
algorithms and end-user quality cannot be certified from local tests.


### Final validation and retained failures

The final ordinary Go run passed **2,011 cases/subcases**, with 12 explicit opt-in
skips. Focused race tests, **349 frontend tests**, typecheck, vet, Go build and
frontend/panel builds passed. The 36 natural-speech scenarios pass, including an
asserted before/after bandwidth-constrained comparison. Those tests exercise
both the actual codec and shared production decode/release code.

The final 60-second Chromium comparison used identical source hashes, the same
96 kbps jitter/loss profile and seed:

| Upload policy | Upstream marker loss | Upstream p95 | Downstream marker loss | Outcome |
|---|---:|---:|---:|---|
| Standard | 11.02% | 182 ms | 0% | Failed original marker gate |
| Guarded | 3.39% | 186 ms | 0% | Passed original marker gate |

This supports further controlled testing of guarded mode, not its automatic
promotion. Earlier short comparisons did not establish a benefit. All failed
runs remain evidence; lower marker loss is not itself a validated speech/MOS
improvement. The guarded implementation was also corrected after real browser
inspection showed that fresh RTCP loss reports need not contain RTT measurements.
When RTCP RTT is absent, only newly observed STUN responses can refresh path RTT.
Repeated reads of a cached candidate-pair RTT cannot establish congestion.

Direct/relay fault tests passed: the impaired direct route reported 399 lost
packets, while the preselected healthy relay reported zero. Direct, TURN/UDP and
TURN/TCP credential renewal passed with zero measured bidirectional loss.

One initial impaired diagnostic check returned `inconclusive` with 78 observations
and measured loss around 15%. The old test incorrectly demanded `degraded` plus
100 observations even though real SCTP congestion can reduce the sample count.
The production classifier correctly avoided `ready` and was not changed. The
test now accepts `inconclusive/insufficient_samples` with actual loss evidence,
while preserving the healthy-path requirements. Three repeated real-browser
probe tests subsequently passed. This test correction does not relax the audio
benchmark's acceptance thresholds.


The final **8 simultaneous outbound browser→Telephony→SIP calls for 60 seconds**
passed with frozen source verified before/after. This uses both PCMA and G.722,
healthy/jitter/180 ms shift/420 ms pause scenarios, actual Chromium capture and a
local TLS/SRTP carrier fixture. Every call passed its existing delay, recovery and
continuity gates. This is a regression/load smoke run, not a proof of sustained
production capacity or a full-day call-centre load. The first run also passed
media gates but was correctly invalidated by the source-integrity guard when a
diagnostic test file changed; the frozen-source rerun is the accepted evidence.

At the end of this verification phase no production/staging configuration, number, process, deployment, release tag or registry entry had changed. The subsequent 0.12.41 release and local test installation are recorded separately.
