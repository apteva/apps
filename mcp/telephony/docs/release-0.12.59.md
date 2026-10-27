# Telephony 0.12.59

Adds optional adaptive playout for continuous AI-agent replies to callers across
SIP and supported carrier-stream bridges. Existing fixed buffering remains the
default; human microphone pacing is unchanged.

Agent audio profiles now configure minimum/maximum reserve, stable-delivery decay,
SIP idle recovery, experimental 0–3% pitch-preserving PCM duration correction,
waveform-match threshold and bounded timing capture. Existing project, direction,
agent, route, account and number scopes, TTL overrides and per-call pinning apply.
Changing a profile affects new calls without redeploying.

Short SIP starvation still resumes immediately rather than adding another startup
wait. Optional speed correction only adjusts strongly matched waveform regions;
silence and unsuitable speech remain unchanged. Queue admission/retries are
transactional and source playback offsets, scoped cancellation and RTP clocks
remain intact. Carrier-stream adaptation uses the existing send-ahead window.

Optional traces correlate provider/Core timing metadata, Telephony receipt and
carrier/RTP writes. Capture is bounded and off by default. Cross-host timestamps
are correlation anchors, not proven network latency; RTP writes are transmission
evidence, not handset playback. Detailed provider/Core timing needs the separately
maintained Core companion changes. Core is not published by this release.

Also corrects the browser integration fixture's CORS allowlist for the shipped
client's X-Request-ID header. The prior fixture rejected call reads before its
network-outage scenarios could begin; production CORS behavior is unchanged.

Validation:
- Full Telephony Go suite, Core short suite, focused race checks, TypeScript/vet
  and 437 frontend/UI tests passed.
- Real-browser 90-second WebRTC/TURN and 120-second PCM outage/recovery
  scenarios passed after the fixture correction.
- Twelve actual local Core → Server → carrier paths passed with adaptation,
  speed correction and tracing enabled, including cancellation and subsequent replies.
- Recorded-speech slowdown replay reduced empty playback from 180 to 60 ms with
  20 ms additional elapsed playback; automated transcription found no word changes
  in the tested comparisons. This is not a subjective listening-quality score.
- Two-minute controlled 2% source-clock drift test eliminated 2.34 seconds of
  empty intervals using optional correction on a periodic signal.
- Repeated live model WebSocket/WebRTC comparisons passed; they did not establish
  a consistent transport winner. The first unexpected 280 ms burst gap remains.

See docs/agent-playout.md for configuration and repeatable tests. This release does
not enable experimental policies, change routes or update any deployment.
