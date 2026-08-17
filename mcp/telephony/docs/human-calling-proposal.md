# Human Calling Proposal

Status: Proposal

Today Telephony can only put an **AI realtime thread** on a call. Both
`telephony_place_call` and `telephony_answer_call` require a `directive` and
call `SpawnRealtimeThread`; inbound `answer_mode` is `agent` or
`realtime_immediate`. A person can watch calls in the panel and hang up — they
cannot talk on one.

This proposes making the far side of the bridge **pluggable**, so a human can
place and receive calls through the same carrier, number, routing, recording,
and lifecycle machinery the agent uses.

## Why this is tractable

Every audio path in the app — Twilio Media Streams, the generic JSON carrier
bridge (SignalWire/Telnyx/Plivo), Vonage, and direct SIP/RTP — converges on one
function:

| Bridge loop | Call site |
| --- | --- |
| Twilio | `bridge_twilio.go:207` |
| Generic JSON carriers | `bridge_carriers.go:107` |
| Vonage | `bridge_carriers.go:413` |
| Direct SIP/RTP | `sip_rtp.go:351` |

```go
bridgeURL, err := a.mediaBridgeURL(row)   // main.go:1894
```

Each loop then dials that WebSocket and speaks a small, symmetric protocol:

```text
telephony -> peer   OpBinary  PCM16 @ 24 kHz   (caller audio)
                    OpText    input.speech_started | playback.progress | playback.overflow

peer -> telephony   OpBinary  PCM16 @ 24 kHz   (audio to play to caller)
                    OpText    audio.frame | interrupt
```

Nothing in those loops knows or cares that the peer is a model. Codec handling,
resampling, VAD, the outbound pacer, playback marks, close-state accounting,
recording, and lifecycle events are all **already peer-agnostic**.

So the core change is one sentence: **make `mediaBridgeURL` able to return a URL
that points back at Telephony itself**, where a browser is attached instead of a
realtime thread. All four bridge loops then work unmodified.

## Design: the peer abstraction

Add a `peer_kind` to the call row:

| `peer_kind` | Far side of the bridge | Status |
| --- | --- | --- |
| `realtime` | Core realtime thread (`AudioBridgeURL`) | today's behavior, the default |
| `human` | Browser session attached to an in-process hub | new |
| `pstn` | A second carrier leg (see Phase A) | new, no bridge loop involved |

`mediaBridgeURL` gains one branch at the top:

```go
func (a *App) mediaBridgeURL(row *callRow) (string, error) {
    if row.PeerKind == peerKindHuman {
        return a.humanPeerURL(row), nil   // ws://127.0.0.1:<port>/peer/<call_id>/<secret>
    }
    // ... existing realtime renew logic, untouched
}
```

`humanPeerURL` is loopback into the app's own HTTP server. A new `NoAuth` route
`/peer/` accepts that internal dial; a second route `/human/` accepts the
browser's WebSocket (proxied by the platform, token-authenticated exactly like
the existing `/media/` routes). A small hub joins the two.

```text
  carrier WS ──► existing bridge loop ──► /peer/<id>  ──┐
  (µ-law 8k)     decode, VAD, resample                  │  hub
                                                        │
  browser  ◄──── /human/<id>/<token> ◄──────────────────┘
  (PCM16 24k over WS)
```

### What the browser peer must do

- **Capture**: `getUserMedia` → `AudioWorklet` → downsample to 24 kHz → Int16 →
  send as binary frames.
- **Playback**: binary Int16 @ 24 kHz → Float32 → jitter buffer (~80 ms) →
  `AudioWorklet` → speakers.
- **Control frames**: ignore `playback.progress` / `playback.overflow` (they
  exist for TTS pacing); never send `audio.frame`. Optionally send `interrupt`
  on push-to-talk to flush queued playback.

Mic audio is already real-time, so the outbound pacer never overflows, and the
`audio.frame`/`interrupt` machinery is simply unused rather than special-cased.

## Phase A — PSTN handoff (cheap, ships first)

Before any browser audio, there is a much smaller win that covers most real
"let me talk to them" cases: **let the carrier connect the human's own phone**.

- **Outbound**: place the call to the *operator's* number, and answer it with
  carrier XML that dials the destination — `<Dial callerId="+from"><Number>+to</Number></Dial>`.
  One carrier API call plus one XML response. The app already generates and
  serves carrier XML (`carrier.go:384` `plivoStreamXML`, `carrier.go:350`
  `handlePlivoXML`).
- **Inbound**: an inbound route with `answer_mode: human_pstn` answers by
  dialing the operator's number instead of spawning a thread.

No bridge loop, no browser audio, no echo cancellation, works on mobile, and
audio quality is plain PSTN. Costs a second carrier leg per call. Add a
`Dial(ctx, row, dest)` method to the `carrierAdapter` interface
(`carrier.go:33`) and implement per carrier; Twilio first, others incrementally
— an unimplemented `Dial` returns "not supported by this carrier" rather than
breaking anything.

This also lines up with the "external number" destination already sketched in
`call-routing-flows-proposal.md`.

## Phase B — Browser softphone

The `peer_kind: human` design above. Delivers in-panel calling with no second
carrier leg, and is the prerequisite for anything richer (listen-in, whisper,
takeover).

### Transport choice

Raw PCM over WebSocket reuses everything the app already has and needs no ICE,
DTLS, STUN, or TURN. The tradeoff is that browser echo cancellation is only
reliable when the far audio goes through a WebRTC path — with plain Web Audio
playback, Chrome's AEC reference is inconsistent across platforms.

**Recommendation**: ship Phase B on raw PCM/WS with a clear "use headphones"
affordance and a mic-mute control. If echo becomes the top complaint, add an
optional WebRTC transport later — `pion/rtp`, `pion/srtp`, and `pion/sdp` are
already dependencies, so `pion/webrtc` is a natural extension rather than a new
stack, and it slots in behind the same hub.

## Data model

One migration, `migrations/017_call_peer.sql`:

```sql
ALTER TABLE calls ADD COLUMN peer_kind TEXT NOT NULL DEFAULT 'realtime';
ALTER TABLE calls ADD COLUMN peer_token TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN peer_user  TEXT NOT NULL DEFAULT '';
ALTER TABLE inbound_routes ADD COLUMN human_number TEXT NOT NULL DEFAULT '';
```

Existing rows default to `realtime`, so every current call keeps its exact
behavior. `directive`, `voice`, `thread_id`, and `agent_id` stay populated for
realtime calls and are empty for human calls.

## Surface changes

**New tools** (existing tools untouched — `directive` stays required on both):

| Tool | Purpose |
| --- | --- |
| `telephony_place_call_human` | Place an outbound call with a human peer. Args: `to`, `peer` (`browser`\|`pstn`), `human_number?`, `recording?`. No `directive`. |
| `telephony_answer_call_human` | Answer a pending inbound call as a human. Args: `call_id`, `peer`. |
| `telephony_routes_set_human_target` | Set a route's `human_number` and human answer mode. |

**New `answer_mode` values**: `human_browser`, `human_pstn`. The validator at
`main.go:2948` gains two cases; `agent` and `realtime_immediate` are unchanged
and `agent` remains the default.

**Panel** (`ui/CallsPanel.tsx`): the Calls view gains a dial field with a Call
button, an Answer button on pending inbound calls, and an in-call strip with
mute / hang up / audio level. The panel currently polls every 10 s
(`CallsPanel.tsx:364`) — inbound ringing needs a shorter poll or an SSE
subscription, since a 10 s notification delay is useless for a ringing phone.

**Manifest** (`apteva.yaml`): add `/human/` (no_auth, token-gated) and `/peer/`
to `provides.http_routes`, register the new tools, bump `version`. Note that
`/health`, `/manifest`, `/mcp`, `/events`, and `/ui/` are reserved by app-sdk —
`/human/` and `/peer/` are safe. The version must be bumped in **both**
`apteva.yaml` and the `manifestYAML` literal in `main.go`.

## Non-breaking guarantees

1. No bridge loop changes. `bridge_carriers.go`, `bridge_twilio.go`, and
   `sip_rtp.go` are untouched — this is the whole point of using
   `mediaBridgeURL` as the seam.
2. `peer_kind` defaults to `realtime`; every existing row and code path behaves
   identically.
3. Existing tools keep their signatures and their required `directive`. Human
   calling is new tool names, not new optional args on old tools.
4. `answer_mode` gains values; the two existing ones are unchanged and `agent`
   stays the default.
5. Recording, lifecycle events, `call.*` topics, numbers, and compliance are all
   carrier-side and need no changes — human calls emit the same events and
   produce the same recordings.

The existing `carrier_parity_test.go` and `hardening_test.go` suites should stay
green untouched; that is the regression signal for guarantee 1.

## Risks

| Risk | Mitigation |
| --- | --- |
| Acoustic echo in the browser | Headphones affordance, mic mute, `echoCancellation: true`; WebRTC transport as the escape hatch |
| WebSocket jitter under load | Adaptive jitter buffer; the pacer already handles the carrier side |
| Platform proxy WS timeouts on long calls | Verify gateway idle timeouts; the carrier `/media/` routes already prove WS proxying works |
| `claimMedia` single-attach assumption | Human peer reconnect must release and re-claim cleanly (`bridge_carriers.go:97`) |
| Sidecar restart drops the hub | Human calls cannot survive a restart the way a realtime thread can; surface it as a dropped call rather than pretending otherwise |
| Consent/recording law for human calls | Recording policy is per-route today and applies unchanged, but two-party-consent jurisdictions deserve an explicit UI notice |

## Rough effort

| Phase | Scope | Estimate |
| --- | --- | --- |
| A | PSTN handoff, Twilio only, out + in | 1–2 days |
| A+ | Remaining four carrier `Dial` implementations | 2–3 days |
| B1 | Peer abstraction, hub, `/peer/` + `/human/` routes, migration, tools | 2–3 days |
| B2 | Browser audio worklets, jitter buffer, panel UI | 3–5 days |
| B3 | Inbound ring notification (SSE or fast poll) | 1 day |

Roughly **one to two weeks** for a solid Phase A + Phase B. Phase A alone is a
couple of days and independently useful.

## Follow-on this unlocks

Once the peer is an abstraction rather than a hardcoded realtime thread, the hub
can fan out to more than one peer — supervisor listen-in, whisper coaching, and
agent-to-human takeover mid-call all become configuration of the same hub rather
than new subsystems.
