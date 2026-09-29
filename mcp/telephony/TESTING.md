# Telephony testing

Run the deterministic app tests on every change:

```bash
apteva test --tier 1,2 .
```

For a checkout outside the root Go workspace overlay, set `GOWORK=off` for
direct Go commands. The inbound incident regressions are in
`inbound_protection_test.go`: Saturday open and after-hours simulation,
answer/speak/hangup command ordering, duplicate webhook IDs, repeated callers,
rotating caller IDs, and suppression before adviser delivery.

Before a live release, call a test route during closed hours and listen until
the complete announcement ends. Inspect the carrier command and callback
sequence, confirm the call has `handling_reason=closed_hours`, and verify no
missed-call pool entry. Then verify a Saturday open call reaches the intended
destination. Use dedicated test numbers; the deterministic tests cannot prove
that Telnyx actually played audible speech.

Tier 1 runs the fast in-process suite. Tier 2 compiles and starts the real
sidecar while deterministic carrier and browser peers exercise HTTP,
WebSockets, audio, reconnection, recording, lifecycle persistence, and app-bus
events. Neither tier requires carrier credentials by default.

## Live carrier profile

The opt-in live profile places one real, billable call between two dedicated
test numbers. The first number must be voice-capable for outbound calls. The
second must have a healthy inbound route to this Telephony install.

```bash
export RUN_TELEPHONY_LIVE_CARRIER=I_UNDERSTAND_THIS_PLACES_A_BILLABLE_CALL
export APTEVA_LIVE_BASE_URL=https://public-apteva.example
export APTEVA_LIVE_API_KEY=...
export APTEVA_LIVE_PROJECT_ID=...
export TELEPHONY_LIVE_FROM_NUMBER=+...
export TELEPHONY_LIVE_TO_NUMBER=+...

apteva test --tier 2 --profile live-carrier .
```

Both numbers must belong to the Telnyx connection bound to the installation.
The source number must have a ready outbound profile. The destination number
must have an enabled, healthy inbound route.

The test refuses to dial until those conditions pass. It creates or updates a
deterministic IVR flow, publishes it, assigns it to the destination number, and
restores the number's previous flow in cleanup. The source number calls the
destination through Telnyx, the real IVR prompt runs, and its timeout branch
selects the browser destination. The test requires the inbound call to pin the
published flow version before it answers that destination automatically. First,
deterministic protocol clients send
distinct PCM tones in both directions and measure signal identity, level,
continuity, cuts, and first-audio latency. Then two real headless Chrome
processes replace those clients. eSpeak supplies different clean speech WAVs
as their fake microphones, and the production `SoftphoneSession`, capture
worklet, worker/WebSocket transport, and playback worklet carry each voice in
turn. This stage requires `bun`, `espeak`, and Google Chrome/Chromium; set
`TELEPHONY_LIVE_CHROME` if the browser is not on the standard path.

After hangup the test verifies the pinned IVR flow/version/destination, durable
initiated/incoming, answered, and completed lifecycle events; waits for the
Telnyx recording callback; downloads the recording through Telephony's
provider-neutral playback endpoint; confirms that both tones exist; and
correlates each recorded speech cadence with its reference WAV. The final test
log contains a
`LIVE_CARRIER_EVIDENCE` JSON object with call IDs and measured evidence. It does
not invoke an LLM or require a person to answer either number.

The two-number profile deliberately uses the IVR timeout path. A second
Call Control leg cannot faithfully emulate caller-originated keypad input:
Telnyx accepts `send_dtmf`, and injected dual-tone audio is present in the
carrier recording, but neither is presented to the far leg's gather detector
as a caller keypress. Signed carrier DTMF webhooks, flow branching, and generic
browser keypad commands are covered by the deterministic integration suite.
A live keypad assertion needs an independent SIP user agent or a number on a
second carrier as the caller.

## Call progress scenarios

The deterministic suite (`go test ./...`) covers the busy, no-answer, and
voicemail outcomes without a carrier:

- `TestTerminationReasonMapper` pins every `termination.reason` mapping,
  including Twilio SIP codes and Telnyx hangup causes.
- `TestTelnyxOutboundRingingIsSynthesized` proves an outbound Telnyx call
  rings after `call.initiated` and that a late callback never regresses an
  answered call.
- `TestAnsweringMachineDetectionRecordsAndHangsUp` feeds machine, human, and
  repeated detection results through the same path the carrier webhooks use,
  with both the `notify` and `hangup` actions.
- `TestOutboundSettingsDriveMachineDetection` checks the project defaults reach
  the carrier dial request and that per-call overrides win.
- `frontend/tests/progress.test.ts` walks the exported softphone through
  placing, ringing, connected, and ended, including pushed `call.status`
  frames and the ringback option.

To exercise the same outcomes against a real carrier, use the live profile
with three destinations: a number that is busy (call it from another phone
first), a number nobody answers, and a number that goes to voicemail with
`machine_detection: detect`. Expect `termination.reason` of `busy`,
`no_answer`, and `completed` with `answered_by: machine`, plus one
`call.machine_detected` event for the voicemail call.


## Telnyx AI carrier activation

`TestCarrierActivation*` covers carrier-confirmed answer state, answered IVR
handoff, rejected commands, lost callbacks, durable deadlines across restart,
late preparation, caller cancellation, migration evidence, and media-based talk
time. A prepared Core session alone must never mark a handoff delivered.

The activation journal allows three command attempts per phase, waits up to ten
seconds for each accepted command's confirmation, and has a thirty-second total
budget capped by the call deadline. Failure ends the call with
`ai_activation_failed` and one callback opportunity. Carrier termination errors
remain visible and are retried; Telephony cannot guarantee remote termination
when the carrier API is unavailable.

`TestTier2AIHandoffCarrierActivation` runs a compiled sidecar with loopback
Functions, Core and Telnyx substitutes. It lets a human offer expire, selects AI,
checks answer-before-streaming through signed callbacks, verifies two-way audio,
and checks that the routing decision is applied only after media connects.

```sh
GOWORK=off go test -short ./...
GOWORK=off go test -race -short -run 'TestCarrierActivation|TestAIHandoff|TestRealtimePreparation|TestReliability|TestTerminalAnnouncement' .
GOWORK=off go test -tags integration -run TestTier2AIHandoffCarrierActivation .
```

These tests make no staging, production or live-carrier requests. They validate
Telephony's protocol sequence, not an actual PSTN call.

## Live human audio quality

See [AUDIO-QUALITY.md](AUDIO-QUALITY.md) for the local regression matrix, measured
jitter results, protocol compatibility, queue limits, directional diagnostics,
and the limits of what local tests can prove. These tests do not use staging or
production and must not be confused with the opt-in live-carrier profile.

## Repeatable softphone network benchmark

Run `bun run benchmark:softphone` for actual Chromium and local sidecar audio
measurements under bandwidth, latency, jitter, retransmission stalls and outage
profiles. See [benchmark instructions](benchmarks/softphone/README.md) for gates,
artifacts, reproducibility and the distinction between a local network model
and real carrier/physical audio quality. This does not contact staging or prod.

## Connected-call duration

See [CALL-DURATION.md](CALL-DURATION.md) for snapshotted limits, setup/media
watchdogs, migration behavior and carrier limits. `TestCallDuration*` advances an
explicit clock across hour boundaries and exercises the actual database and
termination paths; it places no real calls.
