# Human softphone audio tuning

Telephony 0.4.3, reviewed and measured on 2026-09-10. Controlled measurements
below are not end-to-end Flexylead call latency.

The shared browser engine now defaults to 0 dB input gain and a 60 ms initial
playback target. Echo cancellation remains enabled. The 80 Hz high-pass filter,
5 ms lookahead limiter, and -3 dBFS ceiling are retained. Quiet-sine testing at
0 dB versus -6 dB showed the expected 6 dB level increase; full-scale input stayed
under the limiter ceiling. Gain changes do not remove transport latency.

The playback comparison used the actual worklet at 24 kHz, 128-sample render
quanta, and ordered 20 ms PCM packets:

| Initial target | First rendered audio | Steady underruns | 40 ms stall underruns | 100 ms stall underruns |
| --- | --- | --- | --- | --- |
| 40 ms | 21.3 ms | 0 | 1 | 1 |
| 60 ms | 42.7 ms | 0 | 0 | 1 |
| 80 ms | 64.0 ms | 0 | 0 | 1 |

A packet already contains 20 ms of audio, so first rendering is earlier than the
nominal target. The 60 ms setting reduced startup buffering by one packet versus
80 ms in this simulation. Adaptive buffering still increases by 20 ms after an
underrun, up to 160 ms, and decreases after stable playback to a 60 ms floor.
The existing stale-backlog limits and crossfades are unchanged. This supports
60 ms as a conservative trial; it does not establish crackle-free behavior on
all networks or a fixed 20 ms improvement throughout every call.

Human and external peers still return decoded PCM directly, including silence
and sub-frame/quiet input. They do not wait for VAD or accumulate an AI analysis
frame. The human carrier policy remains a 40 ms send-ahead window with its
existing stale-audio limits. A reported 20 ms queue peak is not evidence that
changing that window would improve the call.

Carrier diagnostics now count input packets and samples before choosing the AI
or human path. Stored `carrier_audio_diagnostics.input_audio` contains `frames`,
`samples`, `audio_ms`, and `max_gap_ms`. These are per bridge connection. Arrival
gaps are measured locally at decoded carrier input, not network transit time.
For native SIP this is after its jitter buffer. Pacer `max_queued_ms` and
`dropped_stale_ms`, along with existing browser queues and WebSocket backpressure,
remain independent of the speech analyser.

The log's `frames` now counts actual input packets, including silence;
`analyzed_frames` separately counts the AI's 20 ms analysis frames. Human logs
use `processing=passthrough` and omit unmeasured AI RMS/VAD fields instead of
reporting misleading silence. This counter semantics change matters to any log
consumer previously interpreting `frames` as AI analysis frames.

Flexylead can explicitly set `createSoftphone({ audio: { inputGainDB: 0 } })` when
adopting these defaults. The existing host app may override defaults, and saved
panel preferences remain respected. Verify applied browser diagnostics report
`mic_input_gain_db=0`, echo cancellation enabled, and an initial playback target
of 60 ms. For a real A/B call, compare underruns, drops, queue maxima and microphone
limiter reduction under the same headset/network; measure mouth-to-ear latency
separately. Browser audio constraints are requests and actual support varies.

## Configuring trials from a consuming app

```ts
const phone = telephony.createSoftphone({
  audio: {
    inputGainDB: 0,                  // try -6 for comparison
    echoCancellation: true,
    noiseSuppression: false,
    autoGainControl: false,
    playbackTargetMs: 60,            // try 40, 60, or 80
    // Optional adaptive bounds, each 40–160 ms:
    playbackMinMs: 40,
    playbackMaxMs: 160,
  },
});

// Change a connected call's audio without redialing (brief audio reconnection).
await phone.reconnect({ playbackTargetMs: 80, inputGainDB: -6 });
```

Without overrides the initial/minimum/maximum are 60/60/160 ms. An explicit
40 ms target automatically gets a 40 ms minimum unless a minimum is provided.
Bounds must satisfy min <= target <= max, each finite and between 40 and 160 ms.
Invalid settings fail before dialing or replacing a working connection. Adaptation,
backlog limits and the limiter remain enabled; only the chosen parameters change.
The built-in Telephony panel also provides microphone gain and playback choices,
persisted per browser and applied on the next call. Explicit saved gain overrides
are preserved, so an existing saved -6 dB profile must be changed intentionally.

Carrier pacing is installation-wide, rather than a browser user's privilege:
set `human_audio_send_ahead_ms` in Telephony's app settings to 20, 40, 60, or 80.
The default remains 40. Self-hosted operators can alternatively use
`TELEPHONY_HUMAN_AUDIO_SEND_AHEAD_MS`; the installation value takes precedence.
Unsupported values fall back to 40. Changes affect newly created bridges; existing
bridges keep their chosen policy. Stored `carrier_audio_diagnostics.send_ahead_ms`
records the selected window. Vonage's direct-live path does not use this pacer.
Queue caps and stale-audio disposal remain fixed safety limits.

No Flexylead-specific authorization or behavior is added, and no server or Web SDK
release is required. Consuming apps only need configuration changes if they want
to override defaults or expose their own A/B controls.
