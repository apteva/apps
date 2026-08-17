# In-Panel Softphone Proposal

Status: Proposal

Add a softphone to the Telephony panel so a person can place and answer calls
from the dashboard, using their browser mic and speakers. The AI realtime path
is untouched.

## The seam

All four audio bridges — Twilio (`bridge_twilio.go:207`), the generic JSON
carrier bridge (`bridge_carriers.go:107`), Vonage (`bridge_carriers.go:413`),
and direct SIP/RTP (`sip_rtp.go:351`) — do the same three things:

```go
bridgeURL, err := a.mediaBridgeURL(row)   // main.go:1894
coreURL, err := url.Parse(bridgeURL)
core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
```

…and then speak a small symmetric protocol over that socket:

```text
telephony -> peer   OpBinary  PCM16LE @ 24 kHz mono   (caller audio)
                    OpText    input.speech_started | playback.progress | playback.overflow

peer -> telephony   OpBinary  PCM16LE @ 24 kHz mono   (audio played to the caller)
                    OpText    audio.frame | interrupt
```

Nothing in those loops knows the peer is a model. So: **store a loopback URL in
`audio_bridge_url` instead of Core's**, host that endpoint in this app, and
join it to a browser WebSocket. No bridge loop changes at all.

The loopback port resolves exactly as app-sdk resolves it (`run.go:99-110`):
`APTEVA_APP_PORT` if set, else `runtime.port` from the manifest, else 8080.

## Architecture

```text
                        ┌──────────── softphone.go ────────────┐
 carrier WS ──► existing │  /peer/<call_id>/<secret>  (NoAuth)  │
 (µ-law 8k)     bridge   │            ▲                        │
                loop     │            │   hub[call_id]         │
                         │            ▼                        │
 browser  ◄──────────────│  /softphone/media/<call_id>/<token> │
 (PCM16 24k)             └──────────────────────────────────────┘
```

The hub is `get-or-create` keyed by `call_id`, so either side may arrive first
(the browser normally connects at dial time, before the callee answers). The
hub **outlives the browser session**: if the tab reloads mid-call, the carrier
leg stays up, the caller hears silence, and re-attaching resumes audio. Only a
sidecar restart drops a human call.

`/peer/` is `NoAuth` with the per-call secret in the path — the same trust model
as the already-production `/media/` carrier routes, and the sidecar binds
loopback by default (`run.go:119-126`).

## Exactly what changes

### New files

| File | Size | Contents |
| --- | --- | --- |
| `softphone.go` | ~380 lines | `softphoneHub`, `hubFor(callID)`, `handlePeerSocket`, `handleSoftphoneMedia`, `handleSoftphoneAction`, `peerLoopbackURL`, session-token mint/verify |
| `softphone_test.go` | ~250 lines | Hub fan-in/out, browser reconnect mid-call, token rejection, silence-on-detach, `peerLoopbackURL` port resolution |
| `migrations/017_softphone_peer.sql` | 4 lines | see below |
| `ui/softphone-audio.ts` | ~220 lines | Mic capture, resample 48k→24k, jitter buffer, playback worklet (worklet source inlined and loaded as a Blob URL, so no new static route) |

```sql
-- migrations/017_softphone_peer.sql
ALTER TABLE calls ADD COLUMN peer_kind  TEXT NOT NULL DEFAULT 'realtime';
ALTER TABLE calls ADD COLUMN peer_token TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_calls_peer_kind ON calls(peer_kind, status);
```

### `main.go` — 13 edits

| # | Anchor | Change |
| --- | --- | --- |
| 1 | `manifestYAML`, line 46 | Add `/softphone/` and `/peer/` to `http_routes`; bump `version` to `0.2.0` |
| 2 | `HTTPRoutes()`, line 328 | Add 3 routes (below) |
| 3 | `mediaBridgeURL`, line 1894 | 3-line guard: human calls never renew via Core |
| 4 | `callRow`, line ~3018 | Add `PeerKind string` and `PeerToken string` |
| 5 | `callSelectColumns`, line 3084 | Append `COALESCE(peer_kind,'realtime'), COALESCE(peer_token,'')` |
| 6 | `scanCall`, line 3107 | Append `&r.PeerKind, &r.PeerToken` |
| 7 | `insertCall`, line 3128 | Add 2 columns, 2 placeholders, 2 args |
| 8 | `toolPlaceCall`, lines ~860-935 | Extract the carrier-placement tail into `placeOutboundLeg` (see below) |
| 9 | `answerModeAgent` consts, line 239 | Add `answerModeHumanBrowser = "human_browser"` |
| 10 | answer-mode validator, line 2948 | Accept the third value |
| 11 | `recordInboundCall`, line ~2230 | Set `PeerKind: "human"` when the route is `human_browser` |
| 12 | `recordInboundCall`, line 2266 | Deliver the agent incoming-call event only when mode is `agent` |
| 13 | new `claimPendingCallForHuman`, near line 3221 | Sibling of `claimPendingCall` without the `agent_id` predicate |

Edit 2:

```go
// Browser softphone: panel actions, and the browser's audio socket.
{Pattern: "/softphone/", Handler: a.handleSoftphoneAction},
{Pattern: "/softphone/media/", Handler: a.handleSoftphoneMedia, NoAuth: true},
// Loopback endpoint the carrier bridge dials for human-peer calls.
{Pattern: "/peer/", Handler: a.handlePeerSocket, NoAuth: true},
```

Edit 3 — the entire change to the shared seam:

```go
 func (a *App) mediaBridgeURL(row *callRow) (string, error) {
 	if row == nil {
 		return "", errors.New("call unavailable")
 	}
+	// Human-peer calls bridge to this app's own loopback endpoint;
+	// there is no Core thread to renew.
+	if row.PeerKind == peerKindHuman {
+		return a.peerLoopbackURL(row), nil
+	}
 	if row.MediaStatus != "disconnected" && row.MediaStatus != "error" {
 		return row.AudioBridgeURL, nil
 	}
```

Edit 8 is the only non-mechanical one. `toolPlaceCall` currently interleaves
"spawn the realtime thread" with "insert the row, place the carrier call, update
carrier identity, unwind on failure". The unwind is subtle (`KillThread` +
`carrier.Hangup` + `updateStatus` in three different failure orders), so it
should be shared rather than copy-pasted. Extract lines ~880-935 into:

```go
type outboundLeg struct {
	CallID, ThreadID, To, From, AudioBridgeURL string
	PeerKind, Directive, Voice                 string
	// ... timeout, maxDuration, recording policy, carrier, bound, projectID
	OnUnwind func()   // KillThread for realtime; no-op for human
}

func (a *App) placeOutboundLeg(ctx *sdk.AppCtx, leg outboundLeg) error
```

The realtime path passes `OnUnwind: func() { _ = ctx.PlatformAPI().KillThread(agentID, threadID) }`
and is otherwise byte-identical. The human path passes `ThreadID: ""`,
`AudioBridgeURL: a.peerLoopbackURL(row)`, `PeerKind: "human"`, and a nil unwind.

### `apteva.yaml` — 2 edits

```yaml
version: 0.2.0            # was 0.1.19 — must match manifestYAML in main.go

provides:
  http_routes:
    - { prefix: /softphone/ }
    - { prefix: /peer/, no_auth: true }
```

No new `mcp_tools` and no manifest permission changes — the softphone is
panel-driven over HTTP. `platform.realtime.spawn` is simply not exercised on
human calls. The `answer_mode` enum gains a third value (see Inbound routing);
the two existing values and the `agent` default are unchanged.

## Inbound routing — "this number rings in the browser"

Yes, and it costs less than expected, because `human_browser` is shaped almost
exactly like the existing `agent` mode. Today `answer_mode: agent` already:

- creates the call row with status `pending`,
- tells the carrier to **hold** the caller with a prompt
  (`writeTwilioHold`, `main.go:2140` and `main.go:2469`; Plivo's equivalent at
  `plivo_route.go:184`),
- waits for something to claim it, and
- times out via the route's existing `timeout_sec`.

That hold loop **is** the ringing behavior a softphone needs. No new carrier
work at all. `human_browser` differs from `agent` in only three ways:

| | `agent` | `human_browser` |
| --- | --- | --- |
| Incoming-call event to the agent | delivered (`main.go:2266`) | not delivered |
| `peer_kind` on the row | `realtime` | `human` |
| Claim | `claimPendingCall` (matches `agent_id`) | `claimPendingCallForHuman` (project-scoped) |

The route keeps its `agent_id` for provenance; the human claim just doesn't
predicate on it. `claimPendingCall` is an atomic conditional `UPDATE`, so the
new sibling inherits the same race safety — if two operators hit Answer at once,
exactly one wins.

**Configuring it** follows the precedent the panel already set for transport:
`applyTransport` (`CallsPanel.tsx:950`) mutates a route through
`POST /numbers/transport`. Add `POST /numbers/answer-mode` and a "Ring in
browser" control in the Numbers view, right beside the existing answer-mode
display at `CallsPanel.tsx:1097`. The agent-facing
`telephony_routes_set_answer_mode` tool keeps working; its enum gains the value,
but nothing about `agent` or `realtime_immediate` changes.

### Timeout fallback

A number that rings a browser needs an answer for "nobody picked up". The route
already has `timeout_sec` and the hold loop already re-enters at `main.go:2469`,
so the fallback belongs there. v1 should reject on timeout (the caller gets a
normal busy/hangup).

The natural v2 — and worth designing the field for now even if unimplemented —
is `human_first`: ring the browser for `timeout_sec`, then fall through to the
route's configured agent directive. That gives "a person if one is there,
otherwise the AI", which is probably the mode most numbers actually want.

### Who rings

v1 rings **every panel viewer in the project**, first to answer wins. Routes have
no concept of a user today, so per-operator targeting ("only Jeremy's browser")
would need a user identity on the route — deliberately out of scope, and the
`peer_token` column leaves room for it.

### `ui/CallsPanel.tsx` — 5 edits

| # | Anchor | Change |
| --- | --- | --- |
| 1 | `CallsView`, line 292 | Add a dial row: E.164 input + **Call** button → `POST /softphone/place` |
| 2 | call list rows, line ~505 | On `status === "pending"` inbound rows, add an **Answer** button → `POST /softphone/answer/<id>` |
| 3 | detail pane, line ~544 | Add the in-call strip beside the existing hangup button: mute toggle, input/output level meters, connection state |
| 4 | `loadCalls` poll, line 364 | 10 s → 2 s while any call is `pending` or active, back to 10 s when idle |
| 5 | Numbers view, line ~1097 | "Ring in browser" answer-mode control beside the existing answer-mode display, mirroring `applyTransport` (line 950) |

Edit 4 matters: a 10-second poll is fine for a call log and useless for a
ringing phone. This is the smallest change that makes inbound answerable; SSE
would be better and is a clean follow-up.

The `Call`/`Answer` handlers respond with `{ call_id, media_url }`, and the
browser opens `media_url` directly — the URL is built server-side by the
existing `publicInstalledAppURL()` helper (`main.go:2570`), so it inherits the
exact `wss://…/_install/<id>/…` shape the carriers already use through the
platform gateway.

### Rebuild

```sh
cd apps && bun run scripts/build-panels.ts --app telephony
```

Regenerates `ui/CallsPanel.mjs` + `.map` (`scripts/build-panels.ts`).

## Browser audio contract

- **Capture**: `getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } })`
  → `AudioWorklet` → downsample context rate (usually 48 kHz) → 24 kHz →
  Int16LE → binary WS frame every 20 ms.
- **Send continuously.** Mute sends silence, not nothing. Gating on VAD would
  starve the carrier pacer's timing and complicate the SIP path, which prefers a
  continuous stream.
- **Playback**: binary Int16 @ 24 kHz → Float32 → ~80 ms jitter buffer →
  worklet → speakers. Underrun plays silence rather than stalling.
- **Control frames**: ignore `input.speech_started`, `playback.progress`, and
  `playback.overflow` — they exist for TTS pacing. Never send `audio.frame`.
  Send `interrupt` on mute so queued audio is flushed rather than played late.
- **Autoplay policy**: `AudioContext` resumes inside the Call/Answer click,
  which is a user gesture, so this is satisfied for free.

## Why the agent path cannot regress

1. **No bridge loop is edited.** `bridge_carriers.go`, `bridge_twilio.go`, and
   `sip_rtp.go` are untouched. `carrier_parity_test.go` and `hardening_test.go`
   staying green without modification is the regression signal.
2. `peer_kind` defaults to `'realtime'`, so every existing row and every code
   path that does not check it behaves identically.
3. `telephony_place_call` and `telephony_answer_call` keep their signatures and
   their required `directive`. No MCP tool is added or removed;
   `telephony_routes_set_answer_mode` gains one enum value and loses nothing.
4. `answer_mode` gains `human_browser`. `agent` and `realtime_immediate` keep
   their exact behavior, and `agent` remains the default — every existing route
   row is untouched by the migration.
5. `claimPendingCall` is not modified. Human answering gets a **new sibling**
   method, so the agent claim path keeps its `agent_id` predicate verbatim.
6. Calls appear in the panel with no change at all: `handleListCalls`
   (`main.go:2480`) → `recent(project, 100)` (`main.go:3270`) filters on
   `project_id` only, with no agent or peer predicate. Recording summaries
   attach the same way.
7. Hangup already works: `hangupCall` skips its agent check when `agentID == 0`
   (`main.go:1053`) and `killCallThread` no-ops on an empty `ThreadID`
   (`main.go:1888`). The panel's existing hangup button needs no change.
8. Recording, lifecycle events, and the `call.*` topics are all carrier-side —
   human calls emit the same events and produce the same recordings.

## Risks

| Risk | Severity | Mitigation |
| --- | --- | --- |
| Acoustic echo — speaker bleeds into mic | **High** | Browser AEC on, headphones affordance in the UI, prominent mute. If it stays bad, an optional WebRTC transport behind the same hub is the fix (`pion/rtp`, `pion/srtp`, `pion/sdp` are already dependencies) |
| Platform gateway WS idle timeout on long calls | Medium | The carrier `/media/` routes already hold multi-minute WS through the gateway; verify the ceiling before shipping and add app-level keepalive pings |
| Browser sends bursts after a stall → pacer overflow | Low | Hub drops on overflow rather than queueing; `playback.overflow` is already reported by every bridge loop |
| `/peer/` reachable if someone sets non-loopback `bind_host` | Low | Per-call secret in the path, same as `/media/` |
| Sidecar restart drops human calls | Low | Inherent — no thread to reattach to. Surface as a dropped call rather than pretending to recover |
| Two-party-consent recording law | Medium | Route recording policy already applies unchanged, but the in-call strip should show a recording indicator |

## Effort

| Step | Estimate |
| --- | --- |
| `softphone.go` hub + routes + token | 1–1.5 days |
| `placeOutboundLeg` extraction + migration + row plumbing | 0.5–1 day |
| Browser audio (`softphone-audio.ts`) | 1.5–2 days |
| Panel UI (dial, answer, in-call strip, faster poll) | 1 day |
| `human_browser` answer mode + route control + timeout fallback | 1 day |
| Tests + echo/latency tuning on a real carrier | 1–1.5 days |

**Roughly 6–8 days.** The riskiest day is echo tuning, not the plumbing.

## Integration surface for other apps

### Today this is impossible, and not by accident

`telephony_place_call` begins by resolving the calling agent
(`main.go:734`) and hard-errors on zero. The app-to-app bridge in apteva-server
(`apps_callbacks.go:1191`) mints **only** `HeaderBoundCallerInstallID` — it never
forwards `X-Apteva-Caller-Agent`, because a calling app has no agent identity.
So any app calling `telephony_place_call` gets:

```text
could not determine calling agent id — older platform that doesn't forward
X-Apteva-Caller-Agent, or test caller without a Caller in context
```

That is structural: the realtime thread must spawn *inside* a specific agent so
`send`/`done` flow back to it.

### The softphone removes that constraint for human calls

A human call spawns no realtime thread, so it needs no agent — `agent_id = 0` is
already tolerated end to end (`hangupCall` at `main.go:1053`,
`killCallThread` at `main.go:1888`). Human calling is therefore the *first*
telephony capability that is safely callable app-to-app.

### Proposed surface — call intents

Add two tools with `exposure: app_only` (SDK support already exists —
`app.go:88`, gated on the bound-caller header at `run.go:647`), so they are
callable by bound apps and invisible to agents:

| Tool | Behavior |
| --- | --- |
| `telephony_call_request` | Args: `to`, `mode` (`human`\|`agent`), `context?`, plus `agent_id` + `directive` when `mode=agent`. For `human`, parks a **call intent** rather than dialing. |
| `telephony_call_intents_list` | Lets the requesting app reconcile intent → call outcome. |

For `mode: human` the flow is click-to-call, and it reuses the ringing UI this
proposal already builds for inbound:

```text
prospecting/crm ──► telephony_call_request(to, mode=human, context)
                        │
                        ▼  parked intent
                 softphone panel rings: "Call John Smith — Acme, lead #412"
                        │  operator clicks Accept
                        ▼
                 browser attaches ──► telephony dials ──► normal call row
```

Parking rather than dialing is required, not stylistic: on an outbound human
call the browser must be attached **before** the callee picks up, or they answer
to silence.

The `context` blob (a `context_json` column on `calls`) is what makes this
genuinely useful — the softphone can show who is being called and why, and the
requesting app can correlate the resulting `call.*` events back to its own
record.

For `mode: agent`, the caller must pass `agent_id` explicitly, since the
platform will not supply one. That keeps the existing agent path untouched and
makes the caller's intent auditable.

### Caller-side cost

Bound apps need `platform.apps.call` (which `prospecting` already has) plus
`requires.apps: [{name: telephony, optional: true}]`.

One scoping note: `prospecting`'s own manifest states it "deliberately does not
send messages, run campaigns, create opportunities". Dialing is arguably outside
its charter — `crm` or `campaigns` is the more natural first consumer. The
surface above is app-neutral either way.

## Deliberately out of scope

DTMF sending, hold/resume, call transfer, multi-party, agent-and-human on the
same call, SSE push, per-operator ring targeting, and the `human_first` fallback
mode. The hub makes the multi-peer ones natural follow-ups — it can fan out to
more than one peer — but none are needed for a working softphone.
