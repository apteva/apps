# Processes Tier 3 tests

These tests run real Apteva Core agents with the `openai-codex` provider and
`gpt-5.6-terra` by default (`APTEVA_TEST_MODEL=gpt-6-astra` selects Astra). The Processes sidecar is built from this checkout. Each scenario
gets fresh app storage; the runner creates and removes a disposable server.
No Tasks integration is installed and no external publishing service is used.

| Scenario | Persisted outcome checked |
| --- | --- |
| `01-direct-completion.yaml` | Exactly one direct occurrence completes with `Net: 800`. |
| `02-assignment-parameters.yaml` | Two assignments reuse `day-1` independently and retain distinct page parameters and results. |
| `03-human-approval-gate.yaml` | The agent finishes the draft; human review remains waiting and publication remains pending and undispatched. |
| `04-multi-agent-workflow.yaml` | Three distinct agents complete five steps with parallel inputs, a dependency join, explicit agent approval, and a simulated receipt. |
| `05-event-trigger-workflow.yaml` | A duplicate signup publication starts one five-step run across three agents through the real app bus. |
| `06-sequential-worker.yaml` | Three simulated weather/receipt steps complete in order using exactly one persisted worker and one spawn. |
| `07-operator-confirmation.yaml` | A run parks on a human work step, the operator confirms it mid-run over HTTP, and the agent publishes only afterwards. |

Run from the Processes app directory:

```sh
bun run scenarios/run.ts
# One case:
bun run scenarios/run.ts scenarios/04-multi-agent-workflow.yaml
```

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
The fourth fixture enables `setup.app.spawnable` so workers can access Processes,
and requires successful main-thread spawns plus an authoritative read and
completion from a distinct worker for every step. Main coordinates; focused
workers read and complete the assigned steps.

This is a starter suite. Operator confirmation of a human step is covered by
`07-operator-confirmation.yaml`. It does not yet cover operator *rejection*, the
`kind: approval` decision path, Tasks-backed execution, scheduled dispatch, or
delivery fault injection. Those remain covered by deterministic integration
tests, not by these live-LLM cases.

## Recorded smoke run

On 2026-09-12, all three scenarios and the post-run database checks passed using
`openai-codex` / `gpt-5.6-terra`: direct completion (8 iterations), assignment
parameters (11), and the human approval boundary (8). This is one passing smoke
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
as well as the five-step role/approval audit and per-agent tool traces.

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

Workers receive approval evidence directly in `step_get.dependencies`: ancestor
IDs, kinds, states, decisions, outputs, and direct-dependency flags. They should
not need `run_get` or parent confirmation to verify complete approval evidence.
The worker verifier rejects completion on main and missing worker reads/spawns.

## Sequential worker benchmark

`06-sequential-worker.yaml` uses real model calls but simulated Barcelona weather,
conversation and notification receipts. This isolates orchestration; it does not
measure live weather retrieval or external delivery. Use the same CLI, Core,
server, model and fixture for before/after comparisons. The current verifier
requires one persisted worker, one successful spawn, ordered claims/completions,
and exactly one worker `done` after the last step. The unoptimized baseline is
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
assigned agents, parallel inputs, dependency join, approval, saved outputs and
tool-attribution checks passed. Report:
`/private/tmp/processes-worker-multiagent-delimited/run-PGW9i9`.

Local deterministic validation also passed: `GOWORK=off go test -race ./...`
and all 33 UI/outcome-verifier tests. No Core, server or SDK source changes were
needed; the topology regression used the existing topology-capable CLI binary.

## Isolated workers smoke run

On 2026-09-13, the updated five-step scenario passed with `gpt-6-astra`:
203 seconds, 40 iterations, 480,809 reported tokens. All five steps completed
in distinct workers spawned from each assigned agent's main thread; persisted
state, dependency ordering, approval and worker-read assertions passed. The
publication worker used dependency evidence without tool discovery or a parent
clarification. The preceding run stopped at 48 iterations after 242 seconds
with only four steps complete. These are individual smoke runs, not a latency
benchmark or reliability estimate.

## Mid-run operator confirmation

`07-operator-confirmation.yaml` is the only scenario in which something other
than the agent advances the run. The procedure is `draft` → `confirm` →
`publish`, all `kind: work`; the assignment binds `confirmer` to
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
(`delivered_at` and `task_id` empty) and never holding a decision, since a work
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
