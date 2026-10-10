# WebRTC voice quality and concealment

These changes are local Telephony work. They do not install anything, call a
carrier, or modify a staging/production instance. The browser transport remains
optional; PCM/WebSocket remains the default.

## Recovery and codec capabilities

FEC is optional per softphone: `audio: { webrtcFec: false }` opts out, while
omitting the option preserves the existing enabled preference. The Telephony
audio settings offer the same switch for WebRTC/automatic transport. The
preference is applied on a new call or explicit audio reconnect and retained
through automatic recovery; it does not affect another call or restart the
application. Opt-out selects the original portable encoder/decoder and asks
the browser not to send Opus redundancy, while keeping ordinary PLC and the
existing queue bounds. Server codec diagnostics separate `fec_requested` from
actual `fec_enabled`; requesting FEC cannot make an unsupported codec provide
it. PCM/WebSocket audio remains unchanged.

Opt-out verification: the full Go suite passed 1,002 tests/subtests, with five
optional tests skipped; 236 frontend tests passed. Focused race checks covered
FEC selection, two-way WebRTC audio, hold, replacement, caller cancellation,
native fallback and rejection of malformed options while another socket stays
attached. Typechecking, `go vet`, the headless client build and Telephony-only
panel builds passed. No staging/production installation or call was made.

On Linux/macOS amd64/arm64, Telephony tries the system libopus once. An available
library is used for voice encoding with in-band FEC, 20 ms frames, mono,
32 kbit/s initial bitrate, a 24 kHz maximum audio bandwidth matching the input
PCM, and DTX disabled. Readback verifies critical encoder controls before use.
Each call owns its encoder/decoder state. Dynamic loading does not require CGO
or a library at build time. Missing libraries/symbols/failed initialization
retain the existing Go codec. No loading occurs inside a packet callback.

**FEC requires libopus on the runtime host.** Linux package name is usually
`libopus0` (Debian/Ubuntu) or `opus` (Alpine); macOS uses libopus from the system
library search or standard Homebrew paths. This change does not install these
packages. Do not advertise verified FEC on an installation until server
diagnostics show `encoder.capability: libopus_fec`, `fec_enabled: true`.
Fallback reports `go_opus_plc` and `fec_enabled: false`. Negotiated
`useinbandfec=1` alone is not proof of redundant audio packets.

FEC can recover the previous missing packet using redundancy in the next
packet, if it arrives before playback. Native browser playback manages this
in the caller-to-adviser direction. The Telephony decoder now repairs up to
three explicitly missing 20 ms RTP packets in the adviser-to-caller direction:
PLC for earlier losses, a FEC attempt using the next received packet for the
last. A FEC attempt without redundancy may itself produce PLC; ingress
`concealed_ms` therefore counts replacement audio, not guaranteed recovered
speech. Consecutive sequence numbers with timestamp gaps are DTX, not loss.
No repair is invented across large stalls, timestamp resets, duplicates or
unknown frame duration. Existing media queue and stale-audio bounds remain.

## Bounded adaptation

RTCP receiver reports for this sender's SSRC update an atomic feedback value.
They do not run codec controls or database writes. The sender alone adapts at
most once per two seconds, using feedback no older than five seconds. Sustained
loss lowers bitrate in 4 kbit/s steps, to a 16 kbit/s floor. Ten-second healthy
windows restore 2 kbit/s steps, to the original 32 kbit/s ceiling. In-band FEC
has a 10–30% configured loss expectation; codec bitrate includes redundancy.
This is a voice quality/availability tradeoff under congestion, not lossless
encoding or a bandwidth guarantee.

The browser samples existing native statistics. Its sender max-bitrate hint is
bounded to 16–32 kbit/s, reserving a conservative allowance for packet overhead
when a native bandwidth estimate exists. The native WebRTC congestion
controller remains authoritative. Unknown bandwidth does not reduce the rate.

The playback target starts at 60 ms, rises by at most 20 ms per two-second
observation with jitter/concealment, and slowly returns after stable playback.
The default upper target is 160 ms. Explicit existing playback options are
respected, including `playbackAdaptive: false`. `jitterBufferTarget` is an
optional browser hint: unsupported/rejected hints do not stop audio. Requested
target and measured jitter-buffer delay are separate; no universal browser
playout ceiling is claimed. No additional media timers are introduced.

WebRTC monitoring uses a separate observational byte budget. Small messages
are paced through the existing telemetry sender; a write is at most 1,100 bytes
including an estimated framing allowance. The budget is 640 bytes/s unless a
native available-bandwidth estimate supports 2,000 bytes/s. Histories are not
retransmitted in every summary: individual events append to bounded server
history, using current socket ownership checks. Round-robin service and keeping
the in-flight sample intact prevent events/refreshes from starving a complete
transport record. Excess queued observations are bounded and counted locally.
No telemetry is sent before the media handshake is ready. Answer, cancellation
and DTMF bypass the monitoring budget; audio and carrier routing are untouched.

## Observability

Native WebRTC does not expose Telephony's PCM worklet underrun counter.
Headless diagnostics report `underruns: null` on WebRTC. Audio Health omits
that unsupported metric instead of displaying a fabricated zero. Missing
native `concealedSamples` leaves `concealedMs` absent, not zero.

Available native concealment is shown as **WebRTC replacement audio**, with
its own `concealed_audio` filter and warning. It is not classified as exact
PCM underrun duration or guaranteed audible silence. Per-second stats samples
produce bounded timestamped `webrtc_concealment` events, with synthesized
duration, SSRC and observation window. These are sampled detection times;
the native API does not expose each gap's exact start/end. Silent concealment
deltas are retained separately. Baseline samples, changed stats IDs/SSRCs,
counter resets and backwards timestamps never invent new losses.

## Local verification and audible demo

`TestRTCNativeFECControlsAndRecovery` inspects encoded packets for actual
in-band redundancy (libopus 1.5+ inspection API) and exercises decoder recovery.
Codec compatibility covers native and portable encoders. Timeline, bounded
adaptation, unsupported measurements, widget warnings and cancellation tests
are part of the usual suites.

```sh
GOWORK=off go test ./...
bun run test:frontend
GOWORK=off go test -run '^TestRTCNativeFECControlsAndRecovery$' -v .
GOWORK=off go test -run '^$' -bench 'BenchmarkRTCVoiceEncoding|BenchmarkRTCQualityPolicy' -benchmem .

TELEPHONY_SPEECH_INPUT=/absolute/path/24khz-mono-pcm.wav \
TELEPHONY_SPEECH_OUTPUT=/absolute/path/demo GOWORK=off \
go test -run '^TestRTCVoiceSpeechDemo$' -v .
```

The speech replay uses Telephony's production encoder factory and PCM
resampler, with real libopus PLC/FEC decoding. It produces before/after losses
of 20, 120 and 600 ms, plus clean and same-codec PLC controls. All clips use
the same voiced gap location, selected where next-packet FEC is present.
Waveform error relative to the same codec's clean decode is recorded; this is
not a MOS, intelligibility score, physical call or browser jitter-buffer test.
Long losses still lose speech; FEC cannot reconstruct an entire burst.

The real Chromium/Telephony/carrier-substitute network matrix independently
checks both directions, latency, signal level, loss, mute and reconnection.
New forced-TURN/UDP profiles add seeded 1% datagram loss plus jitter, and a
120 ms delivery outage. Their original marker and latency gates are retained.
Neither these modeled networks nor offline speech certify every real network.

The harness records and waits for two advancing synthetic render-clock
observations before arming its network epoch. A context can report `running`
while its render clock is still frozen during device initialization. That setup
wait remains recorded as `audio_setup_warmup_ms`; no delays during a measured
call are subtracted from scores or excused by host load.

```sh
bun run benchmark:softphone --profiles \
webrtc-baseline,webrtc-udp-128k,webrtc-udp-64k,webrtc-udp-48k,webrtc-udp-96k-jitter-loss,webrtc-udp-96k-120ms-burst,local-baseline \
--seconds 20 --seed 20261010
```

## Results from 10 October 2026

The full Go suite passed 996 tests/subtests, with four optional tests skipped.
The frontend suites passed 234 tests. Focused RTC/RTP race checks and `go vet`
passed. CGO-disabled builds passed for Linux and Darwin, both amd64 and arm64;
cross-compilation does not verify native-library runtime behavior on those
other hosts. No installation, carrier call, staging or production change was made.

The 8.62-second French speech replay produced 431 packets, of which 380 had
verified redundancy. At the selected voiced 20 ms gap, same-codec FEC reduced
waveform error by 8.16 dB versus PLC. Selection deliberately requires a packet
with redundancy; this result is not an average over arbitrary losses. The
spoken comparison includes clean speech, before/after 20 ms loss, and
before/after 120 ms loss. Longer missing speech remains unrecoverable.

On this M1 Pro, portable encoding measured 85–89 microseconds per 20 ms frame;
native encoding with FEC measured 183–211 microseconds, with 200 bytes and seven
Go allocations per frame. Native FEC therefore costs about twice as much
encoder CPU; its measured work is approximately one percent of a single CPU
at 50 frames/second. The sampled adaptation check measured 4.28–4.43 ns with
no allocations. These are local microbenchmarks, not server capacity promises.
The subsequent [CPU check](webrtc-fec-cpu.md) includes French speech, decoding,
resampling and paced concurrent workloads; its full codec cost is higher than
the encoder-only measurement above.

The real-browser network results are **mixed**, and should not be advertised
as universal smoothness. Early 20-second runs passed ordinary WebRTC and the
128 kbit/s profile. The final 30-second constrained-network run (seed 20261011)
recorded the following results with unchanged acceptance gates:

| Profile | Result | Adviser → carrier p95 / missing markers | Carrier → adviser p95 / missing markers |
| --- | --- | --- | --- |
| 64 kbit/s TURN/UDP | Failed | 261 ms / 1.7% | 473 ms / 5.2% |
| 96 kbit/s, jitter and 1% datagram loss | Failed | 171 ms / 8.6% | 367 ms / 0% |
| 96 kbit/s, 120 ms UDP interruption | Passed | 148 ms / 1.7% | 368 ms / 1.7% |

A final PCM baseline passed, with 48/236 ms directional p95 and no missing
markers. Marker loss measures identifiable test-tone corruption, not exact
lost speech duration or perceived quality. The host recorded CPU saturation
and scheduling gaps during some runs; this limits attribution but does not
turn failed gates into passes. Clean-host repetition and real-network carrier
validation remain necessary before claiming constrained-network reliability.
