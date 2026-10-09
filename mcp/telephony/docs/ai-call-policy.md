# AI handling duration and inactivity policy

Telephony owns this policy independently of agent prompts and carrier brand.
Only AI sessions newly spawned by this version get a frozen policy. Migration
046 adds a small per-call state table; it does not arm existing sessions.
Human and external calls retain their separate connected-call duration policy.

## Configuration

Installation/project-context settings apply to new calls:

| Setting | Default | Supported values |
| --- | --- | --- |
| `ai_call_max_duration_seconds` | 1200 | 60–14400 |
| `ai_call_inactivity_timeout_seconds` | 30 | 0–300; 0 disables inactivity |
| `ai_call_response_window_seconds` | 15 | 5–120 |

An AI/agent destination can override individual fields in its existing
`config_json`, for example:

```json
{
  "agent_id": 4,
  "directive": "Help callers with their booking.",
  "ai_call_policy": {
    "max_duration_seconds": 1200,
    "inactivity_timeout_seconds": 30,
    "response_window_seconds": 15
  }
}
```

Saving rejects wrong types, nulls, unknown keys, unsupported destination kinds
and values outside these ranges. Missing fields inherit project defaults.
Inbound selection reads the claimed offer or published flow snapshot, then
freezes defaults and overrides before spawning. Startup retries reuse that
snapshot. Outbound AI calls receive project defaults before carrier placement.
Publishing an edited flow/settings affects future spawns only.

## Enforcement

- The AI maximum starts with connected AI media evidence, never a premature
  local answer or Core startup readiness. Its persisted deadline survives
  bridge replacement, worker ticks and process-state loss. Tool execution,
  silence, hold and recovery cannot extend it. The separate shared call limit
  can end a call earlier.
- Inactivity runs only during an explicit Core `listening` phase, with the
  current media bridge connected and playback drained. Existing AI VAD activity,
  Core caller transcript events (text is not retained), and available carrier
  DTMF events cancel pending inactivity termination. The audio waveform/DSP is
  unchanged. JSON stream DTMF and Telnyx's signed DTMF callback are observed;
  this does not add previously unsupported DTMF signaling to other adapters.
- Speaking, thinking, tool work, hold, disconnected/unknown phase and media
  recovery pause inactivity. Interrupted playback cancels the pending reminder.
- After 30 seconds by default, issue one reminder through the existing
  thread-event API. Supportive spawn instructions ask for one short reminder
  in the caller's language. Durable issuance prevents blind replay after an
  uncertain API result or restart; bounded dispatch slots retry admission on
  subsequent ticks instead of accumulating goroutines.
- After reminder audio has actually been written and playback drains, allow
  the full 15-second response window. Carrier marks provide acknowledgement
  where implemented; other bridges use the existing paced playback estimate,
  with a conservative 500 ms tail. This is transport completion evidence, not
  proof that the remote telephone speaker reproduced the sound.
- A reminder that cannot be delivered within the 30-second delivery budget
  while the AI/media phase is available ends with `ai_policy_failure`, separately
  from ordinary caller inactivity. Unknown/unavailable phases pause inactivity;
  the maximum duration remains armed.
- At expiry, refresh the current owner under the existing call-claim lock,
  commit the reason before callbacks can race it, then invoke the generic
  carrier hangup/direct-SIP BYE path. There is no new dial, reroute, number
  disable, application restart or carrier-specific policy switch.
- Carrier hangup failures have at most three policy attempts, with 5/10-second
  retry spacing. Failure remains visibly live with `carrier_termination_failed`
  evidence; Telephony does not invent completion. Provider/network failure can
  prevent a successful hangup. Existing setup/media/shared duration watchdogs
  remain independent safety mechanisms.
- Caller termination or a new human/external owner disarms the AI policy.
  Stale thread and bridge generation observations are ignored. Caller activity
  received before termination commits cancels it; activity after the committed
  ending cannot revive the call.

## Phase availability and overhead

A narrow authenticated subscription uses the existing public, permission-gated
`/api/apps/callback/telemetry` endpoint, one feed per active AI agent. Telephony
requires `platform.telemetry.read`. It subscribes only to `realtime.state` and
`realtime.user` for `tel-` threads. No Core, Server, SDK, carrier integration JSON
or client application change is needed for the policy.

Telephony handles SSE connection boundaries itself because the SDK's channel
hides transient reconnects. Missing permission/endpoint, EOF or a silent stream
invalidate phase state. Reconnection requires fresh state; persisted listening
is never trusted after a process restart. The server's 25-second keepalive is
bounded by a 35-second read watchdog. This feed is ephemeral: it is not a
reliable replay of every Core transition and cannot reconstruct a transition
lost before delivery. The independent maximum is enforced even when the phase
feed is unavailable; inactivity degrades conservatively when state is unknown.

Audio observations perform no SQL or model requests. One compact, indexed
active-policy/ownership query runs per project per second. Durable transitions
are revision guarded against out-of-order writes. Event samples are bounded
at 32. Full call diagnostics remain outside the tick's query.

## Reporting

Normal intentional endings retain terminal `completed`, with
`termination.reason` of `ai_inactivity` or `ai_max_duration`. Late carrier
callbacks preserve these reasons. Reminder delivery failure retains `failed`
and `ai_policy_failure`. Shared panel/headless clients translate these reasons.
The operator audio-health call-detail endpoint exposes `ai_call_policy`, with
policy, frozen deadline, stages, attempts and timestamped transitions. Internal
reminder ownership tokens, transcript text and credentials are excluded.

## Verification and installation

Deterministic tests exercise the actual registration/storage and carrier-command
paths using local platform substitutes, plus phase/playback simulations. Tests
cover response boundaries, tools/hold/recovery, frozen policies, retries,
reconnects/process loss, stale generations, concurrency, provider output shapes,
late callbacks and accurate failure classification.

No staging/production API or real carrier call is used. A live conversational
check remains unperformed. Installing an update replaces the sidecar process;
publication alone does not install it. Drain active calls before a separately
authorized installation.
