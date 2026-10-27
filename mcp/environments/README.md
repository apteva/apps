# Environments

This directory contains the Environments sidecar and its dashboard panel.

## Updates and lifecycle

`environment_update`, HTTP PATCH, and HTTP PUT preserve omitted definition fields.
Fields within `spec` are merged; a supplied array replaces that array, so `[]`
explicitly clears it. Updating an unknown definition returns an error.

Runs whose runtime cleanup fails stay `stopping`. Reconciliation retries cleanup
before permitting a replacement runtime for the same definition. Reconciliation
processes all active runs independently of the 200-entry history list.

Failed provisioning and cleanup attempts use persisted exponential backoff,
starting at 30 seconds and capped at 15 minutes. After five consecutive failures,
the definition enters `degraded` state and automatic reconciliation stops. A
successful running-runtime health check clears the failure streak. An explicit
`environment_start`, dashboard Retry, or definition update clears the terminal
state and permits another bounded sequence. Set
`ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD` to a value from 1 through 1000 to
change the default threshold of five.

## Seeds

Seeds run in spec order when an environment starts. A seed can consume what an
earlier seed created by putting a reference object anywhere in its `input`:

```json
{"seeds": [
  {"app": "crm", "tool": "contact_create", "input": {"name": "Ada"}},
  {"app": "crm", "tool": "activity_log",
   "input": {"contact_id": {"$ref": "0.contact.id"}, "kind": "note", "body": "hi"}}
]}
```

`$ref` is `"<index>.<path>"`: the index of an earlier seed, then a dotted path
into its result. An empty path (`"0"`) references the whole result. References
resolve anywhere in the input tree, including inside arrays, and a `$ref` object
must carry no other keys.

A reference that cannot resolve fails the run at the seed that carries it rather
than passing an unresolved object to the tool. Referencing a later seed, the seed
itself, or a malformed index is rejected when the definition is written. The path
walks objects only — it cannot index into an array.

## Assertions and voice evidence

`mcp_tool_call` requires `mcp` and matches names qualified by that server. Omitting
`tool` means any tool in that namespace; unqualified names do not count.

The runtime telemetry API returns at most 1,000 events without historical paging.
Assertions return an explicit error when that window is saturated. Voice calls
collect incremental, overlapping windows and deduplicate by event ID, preserving
previously observed events throughout the call. A saturated window marks evidence
incomplete and causes the call to fail rather than publish a misleading result.
The recorded failure explains the missing evidence.

Carrier callback events without a `.failed` suffix represent successful HTTP
acceptance. Rejected or undelivered callbacks include status/error details in a
`.failed` event and fail the simulated call.

Voice completion is a request to finish both sides: the last caller audio must
be forwarded, the receptionist must complete its reply, and paced playback must
be acknowledged before the simulation is complete. `final_reply_contains` can
optionally require case-insensitive phrases in that final reply. There is no
fixed goodbye phrase. A caller disconnect stops additional caller speech while
the target/carrier leg keeps capturing its final reply for up to ten seconds.
An unheard final reply is reported as `caller_closed_prematurely`, with separate
reply completion and playback evidence.

Results include `final_exchange` with generated, forwarded, and acknowledged
audio byte counts, response completion, transcript completeness, and the closing
reason. Transcript turns carry `final` or `partial` status; delayed revisions
replace the same transcript ID rather than adding a second turn. Source/target
transcript counts describe provider segmentation only. Recognition matches
finalized content in order across merged or split receiver turns, ignoring case
and punctuation. `pending_caller_turns` and `pending_receptionist_turns` count
source utterances whose finalized content has no match, rather than subtracting
turn counts. Separate `caller_recognition_status` and
`receptionist_recognition_status` report complete, partial, missing, or unknown
recognition evidence. Different wording remains an explicit recognition gap;
it does not prove audio loss. The legacy
`caller_response_generated_not_delivered` flag now requires measured unforwarded
caller audio, rather than a speech-recognition count mismatch.

After closing, telemetry is polled for persisted thread finalization, with a
separate ten-second deadline and an explicit `finalization_timeout` if the
evidence never arrives. Newer Core provides per-item utterance completion and
transcript metadata. Older Core uses provider-final transcript events, listening
state, playback receipts, and persisted `thread.done` events. A partial simulation
does not erase successful booking or ownership evidence in tool results; these
outcomes should be evaluated separately from simulation completeness.

## Microphone transport and cancellation

`microphone_mode` is `auto` by default. Direct audio follows the negotiated
renderer contract (`audio_protocol=1`, `X-Apteva-Audio-Session`); an explicit
continuous-input requirement takes precedence over continuous-conversation mode.
The simulated carrier supplies continuous microphone input by default. Explicit
`continuous` and `discontinuous` modes are available for controlled fixtures.
Unsupported formats or capability contracts fail setup with an attributable error.

Microphone input uses one paced writer per destination: 24 kHz mono PCM16
(960 bytes per 20 ms), or 8 kHz PCMU (160 samples per 20 ms). Silence is generated
on demand before speech, after the VAD tail, and through backend waits. It is
never queued as catch-up audio or counted as generated speech, activity, playback
or turn delivery. Existing noisy-input VAD commit tails remain intact. Playback
receipts and scoped interruptions preserve their session generation.

Each call exposes `input_transport`, keyed by relay leg, with connection/first
input times, first-input delay, input bytes/frames, idle and speech durations,
smallest/largest gaps and maximum pacing lateness. These measure simulator writes;
they do not by themselves prove receipt at the provider or resolve its startup
errors.

Stopping a run persists `stopping` before cancelling admitted voice operations.
The admission gate and active operation records are durable across serving
processes. Media closes immediately; callbacks, thread finalization and available
results get an independent shared 15-second cleanup budget. Runtime destruction
waits for handlers and persisted active operations, with a 20-second forced
cleanup deadline. Forced, partial and failed cleanup remain explicit; original
voice errors are retained separately. Cancellation also runs the finalizer during
startup and failed carrier setup. An operator-cancelled call is `cancelled` and
emits `environment.voice_call.cancelled`.

Repeated stops preserve the first terminal timestamp. `environment_run_get`
returns stored metadata, voice results and `shutdown` even after runtime deletion,
with an explicit `runtime_state`. Unexpected live lookup/callback errors remain
visible. Evals 0.9.6 consumes this shutdown contract.

## Performance and retention

The reconciliation worker keeps its 15-second interval. It polls lifecycle
metadata without selecting or decoding `spec_json`, including when it reloads
state after recovery or records a failure. Stopped, healthy active, backed-off,
and degraded definitions do not decode specs on idle ticks. An eligible start
loads only that definition's current specification and rechecks its state;
updates take effect on the next start or recovery without a decoded-spec cache.

Benchmark the previous idle path against the metadata path with 20 structured
specifications totaling 4,025,080 bytes:

```sh
GOWORK=off go test -run '^$' -bench BenchmarkIdleReconcileLargeDefinitions -benchmem -count=3
```

The catalog is cached for one minute per install; concurrent refreshes are
coalesced, and independent upstream reads run concurrently. The panel polls live
state every five seconds and catalogs every minute. Fixture decoration is batched.

Set `ENVIRONMENTS_RETENTION_DAYS` on the sidecar to a positive day count to enable
history cleanup. Unset or `0` disables deletion (the default). Each reconciliation
removes at most 100 terminal runs older than the cutoff, including their voice
recordings, call results, fixture state, and events. Active runs, runs with an
unfinished voice call, environment definitions, and reusable snapshots are kept.
If file removal fails, database records are retained so cleanup can retry.

## Validation

From this directory:

```sh
GOWORK=off go test -race -cover ./...
GOWORK=off go vet ./...
cd ui
bun install --frozen-lockfile
bun test
```

From the apps repository root, rebuild the shipped panel:

```sh
bun run scripts/build-panels.ts --app environments
```

## Manual clock

Environment runs use wall time by default. To test date-dependent behavior,
create a run with a manual clock:

```json
{
  "spec": {
    "version": 1,
    "clock": {"mode": "manual", "initial_time": "2030-01-01T00:00:00Z"},
    "http_mocks": [
      {"host": "example.test", "path": "/status", "body": {"phase": "before"}, "expires_at": "2030-01-02T00:00:00Z"},
      {"host": "example.test", "path": "/status", "body": {"phase": "after"}, "available_at": "2030-01-02T00:00:00Z"}
    ]
  }
}
```

Pass this to `environment_run_create`, then call
`environment_clock_advance` with the returned `run_id` and
`to: "2030-01-02T00:00:00Z"`. `environment_clock_get` returns the current
time and advancement history. Advancing to the current time or earlier fails.
An agent in a manual-clock run has a read-only `environment_clock_get` tool.

Integration fixtures accept the same `available_at` and `expires_at` fields.
The start is inclusive and the expiry is exclusive. Dated responses take
precedence over undated fallback responses. Overlapping dated responses for
the same request are rejected. Without a matching response, the runtime uses
its existing network or integration mode behavior.

Cloned apps using the updated SDK can call `ctx.Now()` or
`ctx.EnvironmentTime()` to read the server-owned clock. Apps calling Go's
`time.Now()` directly still see wall time. LLM calls, network deadlines, and
runtime expiry also continue to use wall time. Snapshots retain the clock,
its advancement history, and the fixture rules; restoring one starts a new
run at the captured logical time.

### Terminal evidence and silent connections

`final_exchange.thread_finalization` and `cleanup.thread_finalization` record
thread termination and local transcript persistence separately. The collector
examines every terminal event, including a legacy `thread.done` following a
`realtime.thread_ended` that still reports pending provider transcripts. New
Core receipts explicitly report `local_finalization.status`. A failed explicit
receipt or unrepaired legacy transcript write error remains
`finalization_failed`; missing receipts remain subject to the bounded cleanup
deadline and `finalization_timeout`.

Successfully saving interrupted transcript fragments is clean local shutdown,
while recognition, final reply and playback can still be incomplete. Cancellation
retains its cancelled outcome. Idle incoming audio uses the call context and
configured maximum duration as its policy: a healthy silent WebSocket has no
independent two-minute read deadline, and cancellation closes the read promptly.
