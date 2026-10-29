# Configurable generated-speech playout

These controls apply to continuous AI reply audio in Telephony. They do not change
human microphone pacing or add another Core audio queue. The fixed policy remains
the default; speed correction and frame trace capture are off.

Use `telephony_agent_audio_profile` (or the existing
`POST /numbers/agent-audio-profile` endpoint) to change **new calls**, without
redeploying. Policies are pinned for the call. Existing scope precedence, project
permissions, reset and `ttl_seconds` apply:

`project → inbound/outbound → agent/id → route/id → account/id → number/E.164`

Each override is a complete profile, not a partial merge with a broader scope.
Include any transport, format, interruption and other settings you want to retain.

Example temporary experiment (the number/account/agent must belong to the project):

```json
{
  "scope": "agent/7",
  "ttl_seconds": 3600,
  "config": {
    "mode": "auto",
    "transport": "webrtc",
    "format": "pcm16",
    "interruption": "auto",
    "preprocessing": "analysis_only",
    "speech_start_ms": 160,
    "send_ahead_ms": 60,
    "backlog_ms": 1000,
    "playout": {
      "mode": "adaptive",
      "min_reserve_ms": 60,
      "max_reserve_ms": 200,
      "recovery_idle_ms": 1000,
      "stable_ms": 5000,
      "speed_percent": 0,
      "match_correlation": 0.97,
      "trace_frames": 1000
    }
  }
}
```

`transport` selects the **Core → provider** transport, not the phone's SIP leg.
Provider support and Core's existing media validation still apply. These options
are independent: adaptive mode does not enable speed correction or tracing.

| Playout control | Default | Validation / effect |
| --- | --- | --- |
| `mode` | `fixed` | `fixed` or `adaptive` |
| `min_reserve_ms` | `send_ahead_ms` | At least 20, steps of 20 |
| `max_reserve_ms` | min(200, backlog) | At most min(400, backlog), steps of 20; must include the initial `send_ahead_ms` |
| `recovery_idle_ms` | 1000 | 500–5000; SIP reapplies initial buffering only after this idle interval |
| `stable_ms` | 5000 | 1000–60000; lower learned target by 20 ms after this much stable delivery |
| `speed_percent` | 0 | Integer 0–3; experimental bounded PCM duration correction; 0 leaves samples intact |
| `drift` | (on) | `"off"` disables direct-SIP drift compensation; omit to keep it |
| `drift_percent` | 6 | Integer 0–6 (0 = default); expansion rate when a provider streams slower than real time, doubled while the queue is nearly empty |
| `drift_lead_ms` | 160 | 0–400, steps of 20 (0 = default); buffer the compensation grows to after a call first runs nearly dry |
| `match_correlation` | 0.97 | 0.95–1; conservative waveform matching threshold; additional energy/edge gates always apply |
| `trace_frames` | 0 | 0–2000 combined receive/send records per call; 0 disables capture |

### Drift compensation (direct SIP)

Live realtime providers stream at about real time with no lead, and run slow
over a reply: OpenAI `gpt-live-1` measured 0.965–0.994× with single stalls up to
236 ms on 2026-10-29 (`testdata/agent-audio/openai-live-pacing-20261029.json`).
A fixed reserve cannot absorb a steady drift. Direct SIP therefore lengthens
the AI's audio slightly when its queue runs low:

- Explicit silence is lengthened first (up to 25%), which is inaudible.
- Speech is lengthened WSOLA-style by repeating one pitch period with a
  raised-cosine crossfade, using up to 30 ms of recent audio as look-back. A
  splice must match at correlation ≥ 0.90 (voiced audio), may not create a
  sharper sample step than the audio already has, may not repeat louder audio
  into a quieter frame, and leaves each frame's first and last samples intact.
- Healthy delivery is never modified. Compensation acts only when the queue is
  nearly empty (≤ 20 ms) outside the initial reserve fill. That event also arms
  `drift_lead_ms` for the rest of the call, so later stalls find a fuller buffer.
  Playback still starts after the normal initial reserve.

On the six captured runs (18 s of continuous speech each) this reduced queue
underrun gaps from 31 (2.2 s) to 5 (0.3 s), with no click edges or loud
restarts and no pitch change on voiced splices. Carrier media streams (Twilio,
Telnyx and others) are unchanged.

Unknown fields and invalid values fail validation. Removing an override restores
the next applicable scope; it does not mutate active calls.

## Playback behavior

**SIP:** Learn delivery deficits from incoming bursts. Core's adjacent 20 ms
writes within 5 ms are grouped, so splitting a healthy 100 ms burst does not
create a false 80 ms jitter estimate. Raise the target within the configured
bounds; decay it during stability. Explicit silence does not train the estimator.
The learned target applies at initial/long-idle starts. A short starvation resumes
immediately, without another full priming delay. Buffer-only mode cannot rebuild
reserve mid-speech without adding a pause; it deliberately does not do that.

**Twilio and JSON/binary carrier streams:** Adapt their existing carrier send-ahead
window, not a second local startup buffer. This can only send audio that is already
available. Remote residence is estimated from scheduled duration, not measured
handset playback. Existing mark/estimated receipt distinctions remain intact.

**Optional speed correction:** Apply a small pitch-synchronous overlap/add in PCM
before encoding. Repeat/remove only a strongly matching periodic region with an
edge check, retaining chunk endpoints. Leave silence, short chunks, noise or
unmatched speech unchanged. Accumulate at most 10 ms correction debt, bounded by
the selected average rate; reset debt on identity changes, silence and clear.
This is not a general concealment algorithm and cannot reconstruct missing words.

SIP still emits normal 20 ms RTP packets and the negotiated RTP clock. Resampler
and encoder state are not restarted at ordinary chunk boundaries. Playback source
offsets retain the original speech timeline even when rendered duration changes.
A successful SIP write remains transmission evidence, not proof of audibility.

Admission is transactional: an overflow/retry cannot consume source samples,
change estimator/rate state or produce duplicate audio. Receipt/clear handling
continues to reject cancelled generations and preserve subsequent replies. Larger
backlogs still use the existing bounded pressure handling rather than discarding
arbitrary words.

## Diagnostics

`agent_audio.playout` reports current/maximum target, delivery deficit, adjusted
and unmatched frames, and expanded/compressed milliseconds. These are processing
measurements, not missing-speech counters.

`agent_audio.timing_trace` records `telephony_received`, `rtp_transmitted` or
`carrier_write`, with generation/item ID, source end, duration, silence flag and
local write duration. `trace_omitted` makes the capture limit visible. No PCM or
credentials are captured. Receive records include optional Core timing metadata
when Core supports the `playback.ready.timing_trace` opt-in:

- Provider frame receipt timestamp.
- Core write-start timestamp (not write completion).
- Provider receipt → Core write-start and Core queue durations, both measured on
  Core's own clock.
- Each split frame's source duration.

Core's existing renderer-flow events retain actual write duration and credit
wait. Correlate by generation, item and source offsets. UTC anchors on different
hosts must **not** be subtracted as proven network latency without clock-error
bounds. Trace-off is compatible with older Core versions.

## Repeatable checks

```sh
bun run test:agent-playout
GOWORK=off TELEPHONY_AGENT_REPLAY_OUTPUT=/tmp/ai-playout-replay \
  go test -run '^TestAgentPlayoutRecordedSpeechComparison$' -count=1 -v .

bun run benchmark:agent-audio --core /path/core --server /path/server \
  --output /tmp/ai-playout-pipeline --playout adaptive --speed 3 --trace 2000

# Identical recorded caller prompt, actual local Core/Server/Telephony and RTP.
# Supply OPENAI_API_KEY securely in the environment. No PSTN calls/deployments.
TELEPHONY_LATENCY_SIP=1 bun run benchmark:agent-audio \
  --core /path/core --server /path/server --output /tmp/ai-playout-ws \
  --live true --latency true --transport websocket --path sip-G722/outbound \
  --samples 3 --playout adaptive --speed 3 --trace 2000
# Repeat with --transport webrtc and a fresh output directory.

# Optional external ASR agreement proxy on the public speech replay fixtures.
# This is not a listening-quality score and does not measure scheduling gaps.
bun benchmarks/agent-audio/quality.ts /tmp/ai-playout-replay
```

The bench records source hashes, applied settings, telemetry and measured delays.
Live responses are stochastic; repeated trials and a larger varied speech corpus
are needed before promoting a mode. ASR agreement and sine-wave pitch tests do not
establish subjective listening quality. See the dated verification report for
measured improvements and cases that remain unimproved.
