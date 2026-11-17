# Processes Tier 3 tests

These tests run real Apteva Core agents with the `openai-codex` provider and
`gpt-5.6-terra` by default (`APTEVA_TEST_MODEL=gpt-6-astra` selects Astra). The Processes sidecar is built from this checkout. Each scenario
gets fresh app storage; the runner creates and removes a disposable server.
No Tasks integration is installed and no external publishing service is used.

## Core suite (default)

Eleven scenarios, one per behaviour the app must keep. `bun run scenarios/run.ts`
runs exactly these.

| Scenario | Persisted outcome checked |
| --- | --- |
| `01-direct-completion.yaml` | A procedure without steps runs once on the owner's thread and completes with `Net: 800`. |
| `02-assignment-parameters.yaml` | Two assignments reuse `day-1` independently and keep distinct parameters and results. |
| `04-multi-agent-workflow.yaml` | Three agents complete five steps: parallel inputs, a dependency join, review, a simulated receipt. |
| `05-event-trigger-workflow.yaml` | A duplicate signup on the real app bus starts exactly one five-step run. |
| `06-sequential-worker.yaml` | Three ordered steps complete on one persistent worker with one spawn. |
| `07-operator-confirmation.yaml` | A run parks on a human step; the operator confirms over HTTP and only then the agent publishes. |
| `13-step-by-step.yaml` | Nothing dispatches until each step is released (over MCP and HTTP); parallel branches and the human approval hold. |
| `14-run-memory.yaml` | A second run reuses the first run's checkpoint and ledger references instead of repeating discovery; human approval still holds. |
| `15-process-patch.yaml` | `patch` saves only the changed fields as a new version and rejects a stale edit. |
| `17-worker-cleanup.yaml` | Persistent workers complete, fail and park; Processes stops finished workers and their pending wake timers. |
| `20-trigger-discovery.yaml` | The agent finds which app event can start an assignment with `trigger_sources`, uses the suggested mappings and a score filter; among four real pipeline events only the qualifying lead starts a run. |

## Extended suite

`scenarios/extended/` holds the narrower cases: the human approval gate (03,
covered in the core suite by 07), browser continuity (07), executor continuity
(10), MCP response recovery (11), AI parallel steps (12), compact handoffs (16),
ratings (18) and the worker provider (19, needs `--extra-provider`, so it runs
only by name).

```sh
bun run scenarios/run.ts                                   # core suite
APTEVA_TEST_SUITE=extended bun run scenarios/run.ts        # extended (or =all)
bun run scenarios/run.ts scenarios/04-multi-agent-workflow.yaml
bun run scenarios/run.ts scenarios/extended/12-ai-parallel-steps.yaml
```

`APTEVA_TEST_TOKEN_SCALE` multiplies every token budget (default 1 on Codex,
2 elsewhere). `bun run scenarios/compare.ts <before-run-dir> <after-run-dir>`
compares two reports scenario by scenario (add `--md` for a table).

## 0.19.4 vs the simplified protocol (2026-11-17)

Both versions ran this core suite on `openai-codex` / `gpt-6.1-terra` with the
same CLI, Core and server, token budgets ×3 so no run was cut short by a cap.
Each version used its own scenario files (0.19.4's workers still had to call
`step_get`). Both passed all ten scenarios, live asserts and saved-state checks.

| scenario | before | after | iterations | tokens | tool calls | processes_* calls | failed calls | seconds |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| assignment-parameters | pass | pass | 8 → 8 | 186822 → 179527 | 14 → 13 | 14 → 13 | 2 → 2 | 56 → 48 |
| direct-completion | pass | pass | 9 → 9 | 207822 → 191175 | 9 → 9 | 9 → 9 | 1 → 1 | 48 → 44 |
| event-trigger-workflow | pass | pass | 36 → 26 | 895105 → 581886 | 40 → 26 | 29 → 15 | 3 → 0 | 145 → 88 |
| multi-agent-workflow | pass | pass | 32 → 21 | 785372 → 457149 | 36 → 20 | 27 → 12 | 5 → 1 | 131 → 67 |
| operator-confirmation | pass | pass | 20 → 17 | 523046 → 380851 | 20 → 17 | 17 → 15 | 1 → 1 | 85 → 73 |
| process-patch | pass | pass | 7 → 7 | 154660 → 151194 | 7 → 7 | 7 → 7 | 1 → 1 | 53 → 43 |
| run-memory | pass | pass | 41 → 39 | 1115717 → 919285 | 44 → 41 | 39 → 40 | 2 → 2 | 210 → 195 |
| sequential-worker | pass | pass | 15 → 15 | 368519 → 330338 | 15 → 15 | 14 → 14 | 1 → 1 | 66 → 68 |
| step-by-step | pass | pass | 42 → 45 | 1397535 → 1366393 | 46 → 48 | 27 → 29 | 1 → 2 | 242 → 232 |
| worker-cleanup | pass | pass | 27 → 27 | 682866 → 587892 | 31 → 29 | 23 → 20 | 4 → 4 | 106 → 110 |
| TOTAL | 10/10 | 10/10 | 237 → 214 (-10%) | 6317464 → 5145690 (-19%) | 262 → 225 (-14%) | 206 → 174 (-16%) | 21 → 15 | 1142 → 968 (-15%) |

"Before" is release 0.19.4, "after" is this branch. One sample per scenario: single
scenarios move by ±1–3 iterations between runs. The two multi-agent workflows,
where workers previously fetched their step and dependencies, show the large
difference, in both full comparison runs: about 35% fewer iterations, 40% fewer
tokens, half the Processes calls and almost no failed calls. Worker briefs carry
the step and earlier results, so a worker acts and reports with one
`step_update`.

What changed in the app at the same time (`git diff processes/v0.19.4`):

| | 0.19.4 | now |
| --- | ---: | ---: |
| MCP tools agents see | 49 | 42 |
| Tools given to an isolated worker | 13 | 4 (+6 only for memory procedures) |
| Worker protocol | claim/get → work → update (+ reads) | brief → work → one `step_update` with `outcome` |
| Agent skill (`how-to-use-processes.md`) | 25,283 bytes | 6,894 bytes |
| Go source (excluding tests) | 11,196 lines | 10,784 lines |
| Tier 3 scenarios in the default run | 17 | 10 (+8 extended) |

Reports: `cmp-final-old/run-8r9w3O` and `cmp-final-new/run-0yy96i` under the
local Tier 3 artifacts root. Recheck one with
`APTEVA_TEST_VERIFY_DIR=<report> bun run scenarios/run.ts`.

Known issue, unchanged from 0.19.4: the first `run_advance` call in
`13-step-by-step` often comes back to the agent as `context canceled` even though
the release was saved; the scenario keeps its idempotent retry for that reason.

The wrapper invokes `apteva test` with per-scenario limits and a cumulative cost
ceiling of $7.50. `APTEVA_TEST_PROVIDER` selects the provider (default
`openai-codex`) and the model default follows it — `gpt-5.6-terra` for
`openai-codex`, `kimi-k3` for `opencode-go` — with `APTEVA_TEST_MODEL`
overriding. `opencode-go` is connection-backed and needs `OPENCODE_GO_API_KEY`. Provider usage
is real; reported dollar cost depends on provider telemetry. Configure the
runner's existing `OPENAI_CODEX_ACCESS_TOKEN` and optional
`OPENAI_CODEX_ACCOUNT_ID` environment variables (or `~/.apteva/test.env`). Never
commit credentials. `APTEVA_TEST_CLI` can point to a built CLI binary, with
`APTEVA_SERVER_BIN` and `APTEVA_CORE_BIN` pointing to the matching runtime binaries.

`APTEVA_TEST_ARTIFACTS_DIR` selects the report root (default
`/tmp/processes-tier3`). Each invocation creates a fresh subdirectory with
`results.json`, per-scenario app databases, and `verified-state.json`.
The wrapper copies the YAML into that directory and sets the SDK's existing
`DB_PATH` override so only the test app's state survives runner cleanup. It then
opens those databases read-only and checks run counts, saved parameter bindings,
results, role kinds, and step delivery state. The server database and provider
credentials are not retained by this wrapper.

The YAML checks real tool usage and live HTTP procedure/assignment state. The
wrapper adds full persisted-run verification because Core deliberately truncates
tool-result telemetry to 1,000 bytes and the CLI cannot interpolate generated
process IDs into assertion URLs. A model's success claim or completion attempt
cannot satisfy those checks. Use `bun run scenarios/run.ts` for full validation;
raw `apteva test` runs only the YAML assertions.

The fourth scenario uses `setup.topology.nodes` with a primary coordinator and
two responder agents (writer and reviewer). Use a topology-capable CLI build
through `APTEVA_TEST_CLI`. Generated `${PRIMARY_AGENT_ID}`, `${AGENT_writer_ID}`,
and `${AGENT_reviewer_ID}` values bind workflow roles to the actual instances.
Every agent uses the selected model. The CLI must persist its model override
for connection-backed provider configuration, otherwise server defaults can replace it. Research and audience steps become
ready together; drafting waits for both; review gates simulated publication.
The verifier checks audit ordering, frozen roles, distinct executor IDs, and
successful read/completion calls attributed to the assigned agent.
The fourth fixture enables `setup.app.spawnable` so the Processes app can create
workers through the platform thread API. Each worker inherits its executor
agent's spawnable MCP servers, reads the authoritative step, and completes it;
the model does not need to spawn or assign workers itself.

This is a starter suite. Operator confirmation of a human step is covered by
`07-operator-confirmation.yaml`. It does not yet cover operator *rejection*, the
native approval types, Tasks-backed execution, scheduled dispatch, or
delivery fault injection. Those remain covered by deterministic integration
tests, not by these live-LLM cases.

## Recorded smoke run

On 2026-09-12, all three scenarios and the post-run database checks passed using
`openai-codex` / `gpt-5.6-terra`: direct completion (8 iterations), assignment
parameters (11), and the generic human boundary (8). This is one passing smoke
run, not a measured reliability rate. The verifier unit tests also pass.

The five-step, three-agent scenario also passed on 2026-09-12: 24 aggregate
iterations, 341,939 reported tokens, approximately 92 seconds. Saved state and
agent-attributed telemetry both passed verification. Publication was a local
simulated receipt; no Tasks app or external publisher was installed.

## Event-triggered collaboration

`05-event-trigger-workflow.yaml` creates and previews a trigger, then publishes
the same signup twice through a separate test app and the real platform bus.
Exactly one run must complete across three Terra agents. Direct `start` and
`trigger_test_run` calls are forbidden in this scenario. Post-run checks require
one persisted bus receipt, event-mapped parameters, and a linked completed run,
as well as the five-step role/review audit and per-agent tool traces.

Use the updated server and a CLI with topology dependency support. The wrapper
copies Processes into the report directory and adds the isolated test publisher
as a dependency only in that copy; the production app has no publisher or test
MCP tool. Run it with:

```sh
bun run scenarios/run.ts scenarios/05-event-trigger-workflow.yaml
```

Recorded on 2026-09-13: the event-triggered scenario passed all YAML, saved-state,
and agent-attribution checks with `openai-codex` / `gpt-5.6-terra`: 30 aggregate
iterations, 484,305 reported tokens, approximately 144 seconds. Two publishes of
the same signup produced one event record and one completed five-step run.

Workers receive dependency evidence directly in `step_get.dependencies`: ancestor
IDs, states, outputs, and direct-dependency flags. They should not need `run_get`
or parent confirmation to verify completed predecessor evidence.
The worker verifier rejects completion on main, missing worker reads, and any
model-facing assignment of an app-provisioned worker.

## Sequential worker benchmark

`06-sequential-worker.yaml` uses real model calls but simulated Barcelona weather,
conversation and notification receipts. This isolates orchestration; it does not
measure live weather retrieval or external delivery. Use the same CLI, Core,
server, model and fixture for before/after comparisons. The current verifier
requires one persisted worker, ordered claims/completions, and exactly one
worker `done` after the last step. A legacy model-spawn trace remains accepted
for historical artifacts, but the current path has no model spawn. The unoptimized baseline is
checked for saved outputs and ordering without the worker-reuse requirement.

Recorded on 2026-09-13 using `openai-codex` / `gpt-5.6-terra`:

| App implementation | Whole scenario | Run creation to final step | Iterations | Reported tokens | Workers |
| --- | ---: | ---: | ---: | ---: | ---: |
| Baseline (`b0a4c1f8`) | 98.160 s | 48.566 s | 21 | 245,322 | 3 |
| Worker reuse, initial | 79.506 s | 38.744 s | 18 | 200,269 | 1 |
| Worker reuse, final context-preservation fix | 93.251 s | 53.871 s | 17 | 186,234 | 1 |

The whole scenario includes model-driven procedure/assignment creation and the
final verification. Run execution is measured from persisted `created_at` to the
last step's `updated_at`. Worker reuse consistently removed two spawns in these
samples; final reported token usage was 24% lower. The latency measurements are
mixed, so these individual runs do not establish a reliable speedup or a
reliability rate.

Local reports: `/private/tmp/processes-worker-before/run-twfzkb`,
`/private/tmp/processes-worker-after/run-KFi7Ia`, and
`/private/tmp/processes-worker-final/run-yPBfBW`.

The accompanying multi-agent regression initially used a CLI without topology
support; assignment validation correctly rejected nonexistent responder agents.
Using the existing topology-capable CLI produced five completed steps, but the
research output included the sentence-ending period from its fixture instruction
and failed the exact-output assertion. The fixture now delimits expected strings
and explicitly excludes trailing punctuation; assertions remain unchanged. These
failed attempts are not counted as passing regressions.

The clarified multi-agent regression passed on 2026-09-13: 122.055 s,
38 iterations, 457,742 reported tokens. All five independent workers, three
assigned agents, parallel inputs, dependency join, review, saved outputs and
tool-attribution checks passed. Report:
`/private/tmp/processes-worker-multiagent-delimited/run-PGW9i9`.

Local deterministic validation also passed: `GOWORK=off go test -race ./...`
and all 33 UI/outcome-verifier tests. No Core, server or SDK source changes were
needed; the topology regression used the existing topology-capable CLI binary.

## Isolated workers smoke run

On 2026-09-13, the updated five-step scenario passed with `gpt-6-astra`:
203 seconds, 40 iterations, 480,809 reported tokens. All five steps completed
in distinct app-provisioned workers with inherited agent MCP scopes; persisted
state, dependency ordering, review and worker-read assertions passed. The
publication worker used dependency evidence without tool discovery or a parent
clarification. The preceding run stopped at 48 iterations after 242 seconds
with only four steps complete. These are individual smoke runs, not a latency
benchmark or reliability estimate.

## Mid-run operator confirmation

`07-operator-confirmation.yaml` is the only scenario in which something other
than the agent advances the run. The procedure is `draft` → `confirm` →
`publish`, all generic steps; the assignment binds `confirmer` to
`{"kind":"human"}`. The agent completes the draft and stops. Because
`deliverStep` never dispatches a human executor, `confirm` parks in `waiting`
and `publish` stays `pending`. The operator completes `confirm`, the app's own
reconcile loop releases `publish` to the agent, and the agent finishes.

### Why the confirmation lives in the runner

`apteva test` has no mid-run hook. `seed_mcp_calls` run before the agent starts,
`cleanup_mcp_calls` after it stops, and `prompt` is delivered at the beginning of
the run, so none of them can release a step while the run is parked. The
confirmation therefore runs in `operator-confirm.ts`, beside the CLI child:
it polls the scenario's own app database until `confirm` reports `waiting`,
then completes it.

It completes the step over the sidecar's HTTP surface rather than by writing to
SQLite directly, so the app's real authorization runs. `executorIsActor` accepts
the `operator` actor only for a human executor, completion still requires output
evidence, and it is the app — not the test — that delivers the publish step.

The sidecar's port is **discovered, not pinned**. The CLI assigns
`APTEVA_APP_PORT` from `pickFreePort` and the platform proxies MCP to that port,
so overriding it through `setup.app.env` would silently desync the proxy. The
watcher instead lists listening sockets owned by `sidecar` processes and
identifies the right one by asking each for the run's process ID. The sidecar
binds `127.0.0.1` and its own routes carry no auth — the platform enforces that
in front of it — so no credentials are involved.

### What the verifier proves

`verifyHistory` checks the saved run: the human step completed with
`updated_by=operator`, carrying the operator's evidence, never dispatched
(`delivered_at` and `task_id` empty), since a human
step has none. `publish` must be delivered strictly after the confirmation's
`completed_at`.

`verifyOperatorConfirmation` then reads the append-only `process_step_events`
audit, which is the authority on who released the run: exactly one `operator`
completion on the human step, no `agent:` actor anywhere in that step's history,
and no event on `publish` earlier than the operator's. A model claiming to have
confirmed cannot satisfy these; the agent is not permitted to complete that step
at all.

### Recorded smoke run

Passed on 2026-09-18 with `opencode-go` / `kimi-k3`, the first scenario in this
suite run on a provider other than Codex: 93.6 s, 18 iterations, 318,527 reported
tokens. Reported cost was $0.0000, which reflects this provider's telemetry, not
a free run. Report: `/tmp/processes-tier3/run-Ycvg3U`. This is one passing smoke
run, not a measured reliability rate.

The persisted timeline shows the handoff the scenario exists to prove:

| Time | Step | Actor |
| --- | --- | --- |
| 07:08:42.607 | `draft` completed | `agent:1:main` |
| 07:08:42.609 | `confirm` → `waiting` | `workflow` |
| 07:08:43.143 | `confirm` completed | `operator` |
| 07:08:43.145 | `publish` → `ready` | `workflow` |
| 07:08:43.168 | `publish` delivered | — |
| 07:08:58.839 | `publish` completed | `agent:1:main` |

`confirm` was never dispatched, no `agent:` actor appears in its audit, and the
reconcile loop released the publisher 2 ms after the operator's write. The agent
stayed available across the park and completed the publication on the delivery
event rather than by polling.

```sh
APTEVA_TEST_PROVIDER=opencode-go bun run scenarios/run.ts scenarios/07-operator-confirmation.yaml
```

## Executor worker continuity

```sh
APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/10-executor-continuity.yaml
```

This case deliberately fails the old strict-chain detector: both portrait steps
have only inventory as a dependency, and validation joins all three outputs.
The assignment explicitly selects `worker_continuity=per_executor`. The test-only
fixture app creates one prepared context bound to the trusted agent, project and
worker thread. Subsequent operations must reuse that context: a different thread
is rejected. It persists both simulated portrait identities and distinct digests,
validates them, and accepts one publication receipt after operator approval.
Fixture operations and receipts are real local MCP calls; no external Media or
publishing service is called. The production manifest gains no test dependency.

The independent verifier reads both app databases and checks the frozen policy,
one persisted worker, serialized claims, separate tracked step execution IDs,
exact source/context/artifact/digest receipts, all dependency audit gates, an
actual operator HTTP confirmation, one setup, and no repeated fixture discovery.
Negative tests reject extra workers, mixed receipts, early validation,
self-approval, fabricated receipts, and repeated setup. Go regressions cover
sidecar restart/checkpoint recovery, ambiguous delivery retries, timed waits,
multiple executors, run isolation, and an out-of-order stored graph.

Recorded on 2026-10-05 with `openai-codex` / `gpt-6.1-sol`: the live case and
independent saved-state/tool-trace verifier passed in 26 iterations, 198.392
seconds, with 554,671 reported tokens. Six steps completed, including real
HTTP operator approval. One worker performed all five agent steps, one fixture
tool search, one prepare, two distinct renders, one validation, one publication,
and one final done. Five unique tracked execution receipts retained separate
step attribution. Report: `/private/tmp/processes-executor-continuity-tier3/run-1oiWtC`.
This is one passing live smoke run, not a Media performance benchmark. An earlier
browser-based attempt was blocked by platform screenshot file access and stopped;
it is not counted as a pass. The final fixture isolates worker continuity from
that unrelated screenshot path.

Worker response regressions also verify a single dependency manifest, verbatim
ancestor receipts, default policy/checkpoint recovery after a sidecar restart,
explicit `include_context=false` reuse, compact progress/blocker acknowledgements,
idempotent lost completion replies, and the final approval/done gate. Operator
HTTP and explicit run inspection retain full snapshots. In the large-context test,
a full inspection is approximately 91.8 KB, the worker read is 25.7 KB, and the
read with retained shared context is 14.9 KB; most remaining bytes are the exact
required ancestor receipt. A 13.5 KB submitted output produces a 305–316 byte
completion acknowledgement. These are deterministic payload measurements, not
latency estimates. The live verifier also requires policy reuse on later claims,
completion acknowledgements below 1 KB without echoed context, and one final
`done=true` only after publication.

Compact-response live rerun recorded on 2026-10-05 with `openai-codex` /
`gpt-6.1-sol`: scenario and independent verifier passed in 26 iterations,
222.179 seconds, with 529,359 reported tokens. Later claims explicitly reused
shared policy. All five completion acknowledgements were 305–318 bytes, with
`done=true` only after final publication. Total claim/update response payloads
were 13,547 bytes versus 37,207 in the prior continuity smoke run
(about 64% less). These are live payload measurements with model-authored
procedure text; they do not establish an end-to-end latency improvement.
Report: `/private/tmp/processes-executor-continuity-tier3/run-UT7S7P`.
The preceding compact-response attempt completed the workflow but hit a verifier
false negative because CLI argument telemetry encodes booleans as strings. The
verifier now handles that representation, with regression coverage, and the
complete live rerun above passed.

Clean Processes 0.16.2 release verification on 2026-10-05 also passed with
`openai-codex` / `gpt-6.1-sol`: 26 iterations, 208.216 seconds, 503,064 reported
tokens. Both app databases and tool trace passed the complete independent verifier.
Report: `/private/tmp/processes-release-0.16.2-tier3/run-GMdkqp`. Go race, 73
UI/verifier tests, 19 Playwright tests, fixture tests, TypeScript checks and panel
import validation passed on this release checkout. The first packaging attempt
was rejected before any model iteration due to an unquoted comma in a tool
manifest description; it was corrected before this successful run.

## MCP discovery and receipt recovery

```sh
APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/11-mcp-response-recovery.yaml
```

This case creates two immutable draft revisions with different facts and outcomes.
The agent discovers the procedure through `list`, follows exact version references,
reads current content and historical metadata, executes only revision 2, and follows
the direct update receipt's `run_get` reference to recover its saved outcome. The
independent verifier checks persisted version content and run identity/state/output,
actual version reads, the compact receipt, exact recovery arguments and tool order.
Negative tests reject wrong versions/results, repeated occurrences, oversized
receipts, echoed definitions, invalid references and missing recovery reads.

Go regressions use registered MCP callbacks and compare HTTP objects, exercise
large-context payload reductions, preserve numeric IDs beyond JavaScript's safe
integer range without a JSON round trip, and recover exact historical and frozen
run instructions, checkpoints, approval receipts and output identities. The existing
continuity case also checks exact recovery references on step acknowledgements.

Processes 0.16.3 verification recorded on 2026-10-05 with `openai-codex` /
`gpt-6.1-sol`: the recovery scenario and independent verifier passed in 14
iterations, 92.436 seconds, with 250,220 reported tokens. Live `list` was 790
bytes and the completion receipt was 325 bytes. Report:
`/private/tmp/processes-release-0.16.3-tier3/run-u1HXYM`.

The continuity regression and independent verifier also passed in 26 iterations,
216.428 seconds, with 526,529 reported tokens. All six steps completed with one
worker/context/discovery, exact distinct portrait receipts, validation, real
operator HTTP approval and publication. Report:
`/private/tmp/processes-release-0.16.3-tier3/run-pfrIpr`.
The release checkout passed the full Go race suite, 86 Bun UI/verifier tests, 19
Playwright tests, fixture tests, TypeScript checks and manifest validation.
These live tests verify correctness and payload size, not a latency benchmark.

## AI-directed parallel steps

```sh
APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/12-ai-parallel-steps.yaml
```

The model creates one eight-step procedure and an explicit auto-parallel assignment.
The run owner decides its Core thread arrangement. Two independent 45-second local
artifact operations have transferable inputs; a separate session stays bound to the
owner and requires its first saved checkpoint before the second operation. The
independent verifier requires actual child work and overlapping fixture start/end
timestamps, exact artifact receipts, one shared preparation, separate step ownership,
child checkpoints, serial session operations, dependency joins, real operator HTTP
approval and publication. It accepts either two child workers or useful overlap
between the owner and one child, rather than prescribing a team.

Negative verifier tests reject simulated overlap, mixed/fabricated receipts,
missing child checkpoints, stolen step/session ownership, early joins, self-approval,
missing HTTP confirmation, premature completion, repeated preparation and oversized
updates. Go race regressions cover concurrent claims against the frozen capacity,
in-flight checkpoint recovery after sidecar restart, replay of lost completion
replies, authorization and cancellation. The existing serial continuity scenario
remains the regression for thread-bound work and avoiding unnecessary delegation.

Recorded on 2026-10-05 with `openai-codex` / `gpt-6.1-sol`: the full parallel
scenario and independent saved-state/tool-trace verifier passed in 36 iterations,
347.698 seconds, with 896,167 reported tokens. The owner inspected its Core child
roster, selected two children for independent artifacts, saved their identities as
checkpoints, retained separate receipts, kept both session phases local, validated,
received actual operator HTTP approval and published. Report:
`/private/tmp/processes-ai-parallel-tier3/run-CmVD9b`.

The existing serial continuity regression also passed with the same model in 26
iterations, 244.970 seconds, with 536,020 reported tokens: one worker, one prepared
context, no model-created children, validation and actual HTTP approval. Report:
`/private/tmp/processes-ai-parallel-tier3/run-5WhmuP`.

The first parallel attempt completed the workflow with 44.53 seconds of real child
operation overlap, but its verifier attempted to parse a shortened CLI telemetry
preview as full JSON. The verifier now checks original response byte counts and
uses full JSON only when present; durable state verifies exact outputs. Tests cover
shortened previews and premature done markers. The fresh run above passed end to
end after this correction. These fixture timings are correctness evidence, not a
Media throughput or overall latency benchmark.

Final verification passed: full Go race suite (100.160 seconds), the fixture's Go
race test, 108 Bun UI/verifier tests, 19 Playwright tests, UI and scenario TypeScript
checks, and whitespace validation. Only Processes application sources, its embedded
usage guidance, documentation and local test fixtures changed; Core, Server and SDK
sources were unchanged.

### Step-by-step control (GPT-6.1 Sol)

`13-step-by-step.yaml` prepares a frozen controlled run, verifies the initial held
state through MCP, then advances `prepare` twice with one stable key. The runner's
operator watcher independently observes later ready records staying held for two
seconds and releases them over real HTTP, retrying each identical request. It
releases independent branches, validation, a separate human approval, and finally
publication. All fixture actions remain local simulated artifacts.

The independent verifier reads `process_run_advances`, exact step receipts, worker
ownership and fixture operation timestamps. It rejects automatic mode, duplicate
or mismatched releases, premature delivery/dependencies, worker advancement,
missing held observations and missing HTTP approval. The parallel verifier also
requires actual operation overlap and preserved owner-only session checkpoints.
`control-evidence.json` retains release observations and durable authorization
rows. Configure `APTEVA_TEST_PROVIDER=openai-codex` and
`APTEVA_TEST_MODEL=gpt-6.1-sol` before invoking `bun run scenarios/run.ts
scenarios/13-step-by-step.yaml` with the existing authenticated test environment.

## Targeted process edits

```sh
APTEVA_TEST_PROVIDER=openai-codex APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/15-process-patch.yaml
```

This case creates one draft, edits only a single step's instructions with `patch`,
intentionally submits a stale version, then atomically edits a description and
adds/updates/removes steps and parameters. The agent follows both compact
receipts' exact version references and reads the original immutable version.
No workflow is activated and no assignment/run is created.

The independent verifier compares all three stored definitions, checks untouched
policy/dependency/default fidelity and exact source identity, requires stale
rejection, rejects full-replacement requests and oversized receipts, and checks
actual reread calls. Negative verifier tests cover lost content, stale acceptance,
extra versions, missing rereads and unintended execution. Go regressions also
check invalid-patch atomicity, state/project/version guards, full HTTP inspection,
follow-latest versus pinned assignments, and frozen in-flight run definitions.

Recorded on 2026-10-09 with `openai-codex` / `gpt-6.1-sol`: the final live
case and independent saved-state/tool-trace verifier passed in 7 iterations,
50.862 seconds, with 148,204 reported tokens. Both successful patch receipts
were 292 and 419 bytes. Exactly three immutable versions were saved; the stale
request failed without saving a fourth. Report:
`/private/tmp/processes-patch-verified-tier3/run-tagQfl`.
An initial attempt failed before model actions because optional schema objects
encoded `required` as null; this was fixed to an empty array and covered by a
schema regression. Two fresh live runs subsequently passed. A deterministic
large-definition regression measures 40,940 bytes of full definition versus a
173-byte single-step patch request and 291-byte receipt; this measures payload
reduction rather than overall execution speed.

## Compact handoffs and targeted evidence recovery

```sh
APTEVA_TEST_PROVIDER=openai-codex APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/16-compact-handoffs.yaml
```

The model defines a six-step local artifact workflow. Each agent step rereads its
exact current instructions, saves the exact fixture receipt separately, and
submits a structured compact handoff with evidence references. Later steps
recover a named ancestor, exact artifact evidence and frozen approval policy.
One persistent worker retains prepared tools/context; a real HTTP operator
approval gates publication. No whole-run byte-offset reconstruction is allowed.

The independent verifier checks saved evidence hashes and source/artifact/digest
identities against actual fixture operations, handoff/claim budgets, direct-only
claim dependencies, worker/step attribution, real targeted reads, and completion
ordering. Negative tests reject missing or changed evidence, copied ancestors,
missing recovery, premature done/publication, bypassed approval and repeated work.
Go regressions cover the 4 KiB handoff limit, helpful atomic validation errors,
16 KiB worker/evidence budgets, immutable diagnostic retries, original HTTP audit
objects, large legacy outputs, many-source joins and recovery after restart.

Post-run database inspection permits SQLite journal bookkeeping but enforces
`PRAGMA query_only=ON`. This handles Bun/macOS read-only opens of clean WAL
fixtures without missing uncheckpointed records. Tests verify missing sidecars,
uncheckpointed WAL contents, SQL-write rejection and missing-file rejection.
Actual HTTP approval/controller observations are retained before verification.

Verification recorded on 2026-10-14 with `openai-codex` / `gpt-6.1-sol`:

| Scenario | Iterations | Final artifact directory |
| --- | ---: | --- |
| Compact handoffs and exact recovery | 44 | `/private/tmp/processes-handoff-compact-verified-tier3/run-vvbXJe` |
| Existing executor continuity | 27 | `/private/tmp/processes-handoff-continuity-tier3/run-ttJem7` |
| Actual AI-directed parallel work | 37 | `/private/tmp/processes-handoff-parallel-tier3/run-drx8YT` |
| Controller step-by-step execution | 45 | `/private/tmp/processes-handoff-control-final-tier3/run-6S8uZp` |
| Memory across two runs | 42 | `/private/tmp/processes-handoff-memory-tier3/run-fSujsc` |

All final scenarios and independent saved-state/tool-trace checks passed. Final
compact handoffs were 89–189 bytes; claims were 2267–4292 bytes. The workflow
retained one prepared worker context, five separate agent execution receipts,
exact artifact/source/digest evidence, real operator approval and gated publication.
The 30-step legacy regression measures 601,237 bytes of ancestor/shared context
alone versus a 3558-byte compact worker read; any named ancestor remains readable
exactly in one call. A 7020-byte diagnostic record is represented by a 216-byte
handoff and a 560-byte save acknowledgement in the deterministic regression.
These are payload/correctness measurements, not an end-to-end speed benchmark.

Go short/integration suites, race checks, 167 UI/scenario unit tests, all 44 browser
tests and TypeScript checks passed. The Processes consumer pins app-sdk v0.102.0,
verified by ancestry as the latest tag on origin/main at implementation time.
Core, Server, SDK sources and existing immutable procedure/run data were unchanged.

Two earlier post-run verification attempts hit Bun/macOS `SQLITE_CANTOPEN` after
otherwise completed workflows because clean WAL sidecars were absent. The
journal-capable, SQL-query-only inspection helper fixed this; fresh compact and
controller runs above passed. It never ignores uncheckpointed WAL evidence.

## Terminal worker cleanup

```sh
APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/17-worker-cleanup.yaml
```

One procedure runs three times with distinct frozen parameters: completion,
explicit step failure, and an operator-cancelled blocked run. The independent
watcher observes the real Core workers, verifies normal event-only parking, then
injects an old worker's one-shot 12-second `pace` timer into the disposable blocked
worker. It cancels through Processes HTTP before that timer fires. It requires
acknowledged app cleanup and a durable coordinator notification for each exact
run/agent/thread, no live worker or saved
restart definition, retained worker history and exact receipts, rejected late
Core events, and unchanged history after the cancelled timer's original deadline.
The main agent remains alive throughout observation. Test runtime credentials
are read only from the matching disposable server and never retained in reports.

Migration and race regressions cover atomic cleanup intent, upgrade backfill,
failed shutdown/backoff, recovery from a reopened database, stale delivery and
lifecycle events, live blocked workers, and direct-run main-thread protection.
The continuity verifiers accept either an ordered native `done` or an exact,
durable app shutdown acknowledgement; premature worker completion still fails.

Recorded on 2026-10-22 with `openai-codex` / `gpt-6.1-sol`: the cleanup
scenario passed with three exact terminal outcomes, acknowledged shutdown and
coordinator events, preserved history, and no activity after the cancelled wake.
Report: `/private/tmp/processes-worker-cleanup-notice-tier3/run-XrJx4U`.
The sequential regression passed with three ordered steps on one worker and a
coordinator completion read:
`/private/tmp/processes-worker-cleanup-sequential-final-tier3/run-dnYav3`.
The six-step continuity regression passed with one preparation, one worker,
exact artifacts, validation, real HTTP human approval and publication:
`/private/tmp/processes-worker-cleanup-continuity-verified-tier3/run-ew4Jbr`.
Full Go race tests, 139 Bun verifier tests and scenario TypeScript checks passed.
These are individual live correctness runs, not a production cost benchmark.

## Run ratings

```sh
APTEVA_TEST_PROVIDER=openai-codex APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/18-run-ratings.yaml
```

Two real agents: the coordinator owns and runs the procedure; a separate
reviewer agent is named as its external `quality.reviewer`. One run completes and
one fails. The coordinator tries once to rate its own run and is refused.
Processes sends the reviewer a durable rating request per run; the reviewer reads
each run, rates it, then claims and resolves the failed-run and low-score review
items and reads quality once. The verifier checks the saved policy and reviewer,
that the reviewer had no part in the runs, exact scores, tags and rubric values
under the reviewer identity, delivered requests to the reviewer, review items
only for the failed run, each resolved at revision 3 by the reviewer, and the
order of the calls.

Recorded on 2026-10-30 with `openai-codex` / `gpt-6.1-sol` (0.17.1): passed on
the first attempt in 29 iterations (134 s). The coordinator's self-rating was
refused, both requests reached the reviewer, and the reviewer saved both ratings,
resolved both review items and read quality last. Report:
`/private/tmp/processes-run-ratings-tier3/run-ZxsrTi`.

## Worker provider

```sh
APTEVA_TEST_PROVIDER=openai-codex APTEVA_TEST_MODEL=gpt-6.1-sol bun run scenarios/run.ts scenarios/19-worker-provider.yaml
```

Needs `OPENCODE_GO_API_KEY`, a CLI with `--extra-provider`, and Server and Core
builds with thread provider selection, through `APTEVA_TEST_CLI`,
`APTEVA_SERVER_BIN` and `APTEVA_CORE_BIN`. It only runs when named. The agent
defaults to `openai-codex` and `opencode-go` joins its pool. One procedure has three
assignments: `provider` on the whole process, nothing set, and `step_providers` on
one step. The verifier reads the provider of each worker thread from Core's
`llm.start` telemetry, and requires pinned workers on `opencode-go` only and every
other thread, the coordinator included, on `openai-codex` only. A mixed `per_executor`
assignment must be refused. A Core fallback after a failed request fails this test.
The runner sets `kimi-k3` on the `opencode-go` connection, because Server otherwise
gives every tier the first model upstream lists, which may be unavailable.

Live run on 2026-11-04, with Server `f4d517c` and Core `c64942a` from their provider
branches: 32 iterations, 820,001 tokens, 112 s. Reported cost is $0.0000, which
reflects provider telemetry, not billing.
