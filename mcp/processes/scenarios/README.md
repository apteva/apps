# Processes Tier 3 tests

These tests run real Apteva Core agents with the `openai-codex` provider and
`gpt-5.6-terra`. The Processes sidecar is built from this checkout. Each scenario
gets fresh app storage; the runner creates and removes a disposable server.
No Tasks integration is installed and no external publishing service is used.

| Scenario | Persisted outcome checked |
| --- | --- |
| `01-direct-completion.yaml` | Exactly one direct occurrence completes with `Net: 800`. |
| `02-assignment-parameters.yaml` | Two assignments reuse `day-1` independently and retain distinct page parameters and results. |
| `03-human-approval-gate.yaml` | The agent finishes the draft; human review remains waiting and publication remains pending and undispatched. |
| `04-multi-agent-workflow.yaml` | Three distinct agents complete five steps with parallel inputs, a dependency join, explicit agent approval, and a simulated receipt. |
| `05-event-trigger-workflow.yaml` | A duplicate signup publication starts one five-step run across three agents through the real app bus. |

Run from the Processes app directory:

```sh
bun run scenarios/run.ts
# One case:
bun run scenarios/run.ts scenarios/04-multi-agent-workflow.yaml
```

The wrapper invokes `apteva test --provider openai-codex --model gpt-5.6-terra`
with per-scenario limits and a cumulative cost ceiling of $7.50. Provider usage
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
Every agent uses the selected Terra model. Research and audience steps become
ready together; drafting waits for both; review gates simulated publication.
The verifier checks audit ordering, frozen roles, distinct executor IDs, and
successful read/completion calls attributed to the assigned agent.

This is a starter suite. It does not yet cover operator approval/rejection,
Tasks-backed execution, scheduled dispatch, or delivery fault injection. Those
remain covered by deterministic integration tests, not by these live-LLM cases.

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
