# Telephony 0.11.5

Cumulative release retaining all public 0.11.4 features and both previously
unreleased local Telephony commits (`d5c135e5`, `8f1d479e`). Telephony source and
its registry entry are the only release targets. No SDK, Core, Server,
integration configuration or client app is changed.

## Changes

- Generic AI policy for newly spawned calls: 20-minute maximum by default,
  one reminder after 30 seconds while listening, and a 15-second caller response
  window after reminder playback drains. Project settings and validated
  destination overrides are frozen before spawn and retained through retries.
  Caller speech and supported carrier DTMF cancel pending inactivity; AI speech,
  tools, hold, media recovery and unknown phase pause it. Reconnects/process-state
  loss do not extend the duration deadline. Human/external ownership disarms it.
- Generic carrier hangup/direct-SIP enforcement, three bounded policy termination
  attempts, revision-guarded durable stages, bounded timestamped events and
  explicit `ai_inactivity`, `ai_max_duration`, `ai_policy_failure` reasons. Failed
  carrier commands never invent completion. Late callbacks preserve intentional
  endings; failed AI termination cannot mislabel a later human completion.
- Scoped Core phase collection through the existing public telemetry endpoint,
  with `platform.telemetry.read`, per-agent subscriptions, bounded reads and
  phase invalidation on disconnection. Supportive AI instructions issue a short
  reminder; Telephony owns timers and termination independently of the model.
- Include generic carrier bridge generation ownership, bounded cancellation and
  cleanup, safe replacement streams and optional same-leg Telnyx stream recovery.
  Recovery does not dial, reroute, answer again, disable a number or restart the
  application. Providers without a documented restart command use safe
  provider-initiated replacement.
- Include independent stale PCM dropping and stable 250 ms socket deadlines,
  safe recovery of entirely unsent frames, strict termination after partial
  writes, 500 ms forced close, timestamped transport/drop evidence and distinct
  write timeout accounting. Existing freshness/queue caps are retained.
- Include temporary established-media lookup error handling until the last
  verified lease expires, failure/recovery evidence separate from attachment
  events, and observational shared panel/headless page/device state telemetry.
- Shared headless/panel clients translate AI policy reasons. Operator call-detail
  diagnostics expose the frozen policy and transitions without internal tokens.

Human answer/routing, default transport, PCM/Opus codecs, microphone/playback
DSP, adaptive buffers, freshness limits, listening/coaching and all prior
number controls remain included. The playback Worklet is unchanged.

## Verification

- Final complete Go suite: **921 passing tests/subtests, zero failures**, three
  opt-in skips, with published SDK v0.99.0 and `GOWORK=off`.
- Frontend/audio suites: **224 passed**, zero failures (101 client and 123 audio).
- Broad and final focused race checks, Go vet/build, client and benchmark
  TypeScript checks, packaged headless build and whitespace checks pass.
- Policy simulations cover 8/16/24 kHz caller speech, playback-gated reminder
  completion, response-boundary speech/DTMF, tools/hold/recovery/unknown phases,
  cancellation and human transfer, frozen startup retry configuration,
  reconnect/process loss, stale generations and out-of-order persistence,
  duplicate dispatch/ticks, provider output shapes and actual Telnyx/Twilio
  hangup commands against local platform substitutes.
- Audio policy observations do not wait for an occupied database. The local
  observation microbenchmark measured **45.96–48.70 ns** per input/output pair,
  **zero bytes and allocations** per pair. This measures the new observation
  hooks, not total media processing or carrier/browser end-to-end performance.

The initial decoded-speech test expected a reminder 29 seconds after the
worker restarted its idle timer; corrected assertions cover no reminder at
29 seconds and one at 30 seconds. The final complete suite passes.

Raw local evidence: `/private/tmp/telephony-0115-release-go.jsonl`,
`/private/tmp/telephony-0115-release-race.log`,
`/private/tmp/telephony-0115-frontend.log`.

## Limits and installation

No staging/production API, settings or real carrier call was contacted.
A live conversational/provider check remains unperformed. Ephemeral Core phase
telemetry cannot reconstruct every transition lost before delivery; unavailable
or unknown phases conservatively pause inactivity, while the durable maximum
remains enforced. Caller playback completion uses provider acknowledgement or
bounded paced estimates according to existing adapter capabilities.

Earlier network benchmark failures and prior-release controls remain documented
in `carrier-media-recovery-verification.md`; this release does not claim to
resolve every network outage or prove production audio quality.

Publication does not install or activate the app. An installation update
replaces the sidecar process; drain active calls before a separately authorized
installation.

See `ai-call-policy.md`, `carrier-media-recovery.md` and
`media-write-lease-telemetry.md` for configuration and detailed evidence.
