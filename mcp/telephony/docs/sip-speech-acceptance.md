# SIP speech acceptance scenarios

`TestSIPNaturalSpeechAcceptance` runs by default in `go test ./...`. It sends
two licensed human-speech utterances through production SIP encoding, packet
admission, buffering, decoding, concealment and framing. It covers G.711 A-law,
G.711 mu-law and G.722, with optional resynchronization disabled and enabled.
Packets carry varying recorded speech, unlike the repeated-tone replay. The
complete utterance is drained, including its last word.

## Evidence and acceptance

The reference is a separate clean decoder of the same encoded input. Healthy
playback must preserve the complete utterance byte for byte. Other scenarios
compare actual output PCM near the consumed RTP source position. The comparison
permits only the existing maximum 125 microseconds of sample-clock correction
per frame. It checks positive waveform correlation and normalized error; energy
alone cannot credit repeated, muted or concealed audio as delivered speech.

Metrics:

- **Opening speech preserved:** every active 20 ms region in the first 500 ms
  after speech onset is retained. This conservative first-word proxy does not
  rely on automatic transcription or claim a precisely aligned word boundary.
- **Longest damaged speech region:** consecutive active source frames not
  matched in output. Natural silent pauses do not count as interruptions.
- **Longest playback interruption:** consecutive unmatched/silent output frames
  or absent output while speech is expected. This catches playback pauses even
  when all buffered words are eventually delivered. For absent output the
  expected position uses the clean scheduling baseline; it is an objective
  interruption proxy, not a human annotation of audible cuts.
- **Active speech coverage:** matched active source frames divided by all active
  source frames; each frame can be credited once.
- **Added delay:** maximum and 95th percentile source-to-output time beyond the
  measured 40 ms healthy scheduling baseline. A 60 ms queued startup reserve
  begins playback at 40 ms in this simulation because packet zero arrives at
  time zero. Network transit, devices and browser output latency are excluded.
- **Discard/replacement counters:** retained separately from waveform metrics.

Normal acceptance requires all opening/active speech, no interruptions and at
most 2 ms of measurement tolerance beyond the healthy delay baseline. Ordinary
jitter requires all opening/active speech, no interruptions and at most 40 ms
additional delay. A single lost packet has a separate resilience budget (98%
active coverage, opening speech preserved, <=80 ms interruption and <=100 ms
added delay), not a claim of lossless delivery. Severe 420 ms bursts, 440 ms
startup handler stalls and stable 240 ms shifts must be classified as degraded. Their safety assertions can pass
without their speech-acceptance result passing. An 80 ms batch is also reported
without automatically labeling it smooth based on queue bounds.

Active regions have at least 200 PCM16 RMS (about -44 dBFS); matching requires
correlation >=0.92 and normalized waveform error <=0.4. These are declared test
thresholds, not experimentally validated intelligibility scores. Delay is
reported for matched speech; missing speech remains visible in coverage/gaps.

Negative controls deliberately remove opening speech, mute/remove 200 ms,
substitute repeated speech, add 300 ms of delay and exceed the clock-correction
allowance. The quality detector must reject each.

These are objective continuity proxies, **not MOS, PESQ, word recognition or
proof of imperceptible artifacts**. They do not simulate Telnyx's network,
WebRTC encoding/playout, outgoing microphone startup, a busy CRM, or hardware.
They complement the existing browser, bidirectional transport and load tests.
Use exported WAVs for listening review before changing production audio policy.

## Run and inspect

```sh
GOWORK=off \
TELEPHONY_SPEECH_ACCEPTANCE_RESULTS=/tmp/telephony-speech.json \
TELEPHONY_SPEECH_ACCEPTANCE_AUDIO_DIR=/tmp/telephony-speech-audio \
go test -run '^TestSIP(NaturalSpeechAcceptance|SpeechAcceptanceDetectsRegressions)$' -count=1 -v .
```

JSON reports each scenario's speech acceptance independently of test success.
Optional WAV exports preserve the virtual playback timeline, including periods
without output, and include a clean codec reference. Collection runs only in
tests and adds no work to live calls.
