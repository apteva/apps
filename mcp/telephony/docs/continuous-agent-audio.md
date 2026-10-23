# Continuous agent audio

Local implementation, 23 October 2026. No staging/production configuration,
deployment, or PSTN calls are part of this verification. Release version: 0.12.33. Registry publication does not deploy any instance.

## Behavior

Telephony negotiates renderer capabilities with Core through Server using
`audio_protocol=1` and `X-Apteva-Audio-Session` version 1. Capabilities describe
continuous conversation, provider speech events, required local interruption,
scoped cancellation and receipt evidence. They are independent of tool grants
and provider/model names.

In automatic mode, a compatible continuous session gets:

- Continuous caller PCM, including quiet speech and silence, while the model
  speaks or runs backend work. Speech detection observes audio; it does not
  gate it. Analysis-only preprocessing preserves the forwarded PCM.
- Local interruption detection when the negotiated model lacks input speech
  events. Recent modulation evidence prevents occasional changes accumulated
  across steady music from eventually triggering an interruption. This is not
  an acoustic echo canceller and cannot distinguish every echo/noise condition.
- Playback cancellation scoped to session generation and playback ID across
  packetization, pacing, pending receipts and late frames. New replies survive.
  Core backend work is not cancelled by a playback interruption.
- A small carrier lead and bounded generated-speech queue. Continuous overflow
  rejects the affected playback and reports it to Core; it does not silently
  trim generated words or broadly clear unrelated queued replies.
- Stateful resampling and SIP encoding across ordinary chunks. SIP combines
  partial chunks without inserting silence at every chunk boundary, flushing a
  genuine trailing tail on the next send tick. Different playback IDs are
  never combined in one packet.
- Bounded live/retained cancellation diagnostics. No new periodic worker or
  per-frame database query is added.

Human calls retain their existing loopback, microphone and playback policies.
Missing negotiation preserves the legacy AI policy. Forcing `continuous`
against an incompatible Core/Server fails with an explicit setup error.

## Configuration

Administrator-only MCP tool: `telephony_agent_audio_profile`.
Equivalent authenticated settings route: `POST /numbers/agent-audio-profile`.

Example tool arguments:

```json
{
  "scope": "agent/42",
  "config": {
    "mode": "auto",
    "interruption": "auto",
    "preprocessing": "analysis_only",
    "speech_start_ms": 160,
    "send_ahead_ms": 60,
    "backlog_ms": 1000
  },
  "ttl_seconds": 3600
}
```

| Field | Values / meaning |
| --- | --- |
| `mode` | `auto`, `traditional`, `continuous` |
| `interruption` | `auto`, `local`, `provider`, `off`; controls signaling, never microphone delivery |
| `preprocessing` | `analysis_only` (default), `noise_suppression` (respects the app's suppression setting) |
| `speech_start_ms` | 80–400, in 20 ms steps; default 160 |
| `send_ahead_ms` | 20–200, in 20 ms steps; default 60, for WebSocket carrier playback; SIP retains its 20 ms RTP cadence |
| `backlog_ms` | 200–2000, in 20 ms steps; default 1000 |
| `ttl_seconds` | integer 0–86400; zero means no expiry |

Scopes, least to most specific: `project`, `inbound`/`outbound`, `agent/<id>`,
`route/<id>`, `account/<carrier-binding-id>`, `number/<E.164>`. Here `agent`
means an Apteva AI agent. Account scopes require an authorized carrier binding;
routes retain existing project/permission checks. No scope grants call access.

Each scope stores a complete resolved profile, not a partial field merge. The
most specific active scope wins. Saved profiles are pinned per call. A changed,
expired or reset override affects new calls; an existing call keeps its copy,
including across a carrier bridge replacement. Read with `{"scope":"agent/42"}`;
reset with `{"scope":"agent/42","reset":true}`. `override_active` distinguishes
expired stored settings from active overrides. Database errors are returned,
not silently treated as default settings.

## Interruption and delivery evidence

`playback.ready` is sent after the actual renderer/pacer is ready. Core delays
the opening greeting until that point for negotiated clients.

`playback.progress` includes `session_generation` and one of:

| Evidence | Establishes |
| --- | --- |
| `carrier_mark` | A returned carrier playback mark; marks invalidated by clear are never successful playback |
| `estimated` | Local timer estimate, used where carrier marks are unavailable |
| `rtp_transmitted` | Successful RTP packet write; not remote receipt or audible playback |

`playback.cleared` includes item/generation, local discarded duration, scope
(`local` or `stream`), command-write status, remote confirmation status and any
collateral playback identities/generations. Twilio and Telnyx clear their whole
remote stream buffer; they cannot selectively remove a Core playback ID.
Unaffected local audio is retained when possible. Already-buffered collateral
playbacks are fenced and explicitly reported to Core for recovery. A successful
clear-command write does not set `remote_confirmed: true`.

Vonage reports local cancellation; the app does not claim an unsupported remote
clear. Sinch uses its stream-wide clear. Both binary paths use bounded pacing
for continuous sessions.

`/audio-health` call detail includes `agent_audio_live` while connected. The
retained carrier report contains `agent_audio` after teardown, including the
pinned profile, capabilities, progress evidence, late/silent frame counters and
a bounded clear history. Credentials and raw PCM are not logged there.

## Core and Server cooperation

The implementation includes local changes in Core and Server. Deploying only
Telephony cannot enable the negotiated contract on an older pair. Automatic
legacy fallback remains available.

- Core advertises continuous behavior separately from traditional provider VAD.
- Negotiated microphone input is normalized into 20 ms PCM16/24 kHz frames and
  bounded to 120 ms queued input. Normal queue admission needs no timer or
  allocation. Overload ends the bridge explicitly instead of replaying stale
  microphone speech.
- Live provider audio has a bounded microphone lane separate from control
  commands. Control is prioritized and provider writes have deadlines.
- Negotiated proxy and renderer writes are bounded to 250 ms. Legacy renderer
  sessions retain their existing timeout policy.
- Progress/clear/overflow controls carry generation and evidence. Old-generation
  controls cannot cancel a reply from a later generation.

These changes bound transport stalls. They do not add transparent migration or
reconnection of every failed SIP AI bridge.

## Repeatable verification

From `mcp/telephony`, without Docker:

```sh
GOWORK=off go test ./... -count=1 -timeout 15m
GOWORK=off go test -race -run '^Test(AgentAudio|AudioFrontend)' -count=1 .
GOWORK=off go test -run '^$' -bench '^BenchmarkAgentAudio' -benchmem .
bun run benchmark:agent-audio --core /path/to/core --server /path/to/server
```

The runner compiles actual Core/Server test fixtures using Go overlays. It
starts only loopback processes, uses actual Telephony carrier handlers, and
retains source fingerprints and JSON results. No external carrier is contacted.

Twelve scenarios cover inbound/outbound Telnyx and Twilio media WebSockets,
SIP PCMA/G.722, and Vonage/Sinch binary streams. Each exercises three explicit
interruptions and an automatic interruption from the saved natural-speech clip:
48 interruptions overall. Tests verify continued caller input, local/remote
clear scope, late-frame rejection, next-reply progress and eventual delivery
ledger drain. Additional tests cover partial frames, concurrent SIP enqueue /
cancellation, quiet speech, noise/music/short acknowledgements, rearming,
project isolation, expiry/pinning, malformed settings, bounded receipts,
collateral generations, overflow and blocked sockets.

The paid Live check is separately opt-in:

```sh
# Supply OPENAI_API_KEY securely via the process environment, never in argv.
bun run benchmark:agent-audio --core /path/to/core --server /path/to/server --live true
```

It sends generated caller audio through the Telephony Telnyx media handler,
actual Server proxy and actual Core to real `gpt-live-1`. The model is not given
the caller text. A successful run interrupted the opening greeting, recognized
the spoken request and replied “Vous avez demandé mardi.” Clip start to
Telephony's clear command was about 220 ms. Both directions were saved as WAV.
This is not a Telnyx-hosted or PSTN call, nor a measurement of the last audible
stale sample on a physical handset. The final rolling detector passed the real-model rerun and is also checked
against the saved clip locally through every carrier scenario.

Microbenchmarks show zero allocations for cancellation fencing and unchanged
frontend allocations: two allocations / 1280 bytes per 20 ms frame at 16 kHz.
Host timing varies; these results do not establish a supported concurrent-call
capacity or guarantee acoustic quality on every handset.

See `verification/continuous-agent-audio.json` for the retained run summary.

### Final check results

- Full Telephony suite on SDK v0.105.0: passed (384.458 s).
- Final agent-audio race tests, frontend tests and overflow regressions: passed.
- Core realtime / continuous / renderer suite: passed; focused races passed.
- Server proxy race tests and Telephony `go vet`: passed.
- Telephony, Core and Server builds: passed.
- Final local scenarios: 12 paths, 48 clears; real Live rerun: passed.

A Core test assumed provider session closure was synchronous. It now waits for
asynchronous closure and reads the counter under its lock; runtime teardown
behavior from the parallel Core work is preserved.

The first real-model attempt exposed an immediate-mark acknowledgement error
in the carrier test fixture. The fixture now schedules marks at simulated
playback completion. Failed-run evidence was retained. The final successful
live run uses the corrected fixture, final detector and SDK v0.105.0.
