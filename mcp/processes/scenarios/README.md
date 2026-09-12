# Processes Tier 3 smoke tests

These tests run real Apteva Core agents with the `openai-codex` provider and
`gpt-5.6-terra`. The Processes sidecar is built from this checkout. Each scenario
gets fresh app storage; the runner creates and removes a disposable server.
No Tasks integration is installed and no external publishing service is used.

| Scenario | Persisted outcome checked |
| --- | --- |
| `01-direct-completion.yaml` | Exactly one direct occurrence completes with `Net: 800`. |
| `02-assignment-parameters.yaml` | Two assignments reuse `day-1` independently and retain distinct page parameters and results. |
| `03-human-approval-gate.yaml` | The agent finishes the draft; human review remains waiting and publication remains pending and undispatched. |

Run from the Processes app directory:

```sh
bun run scenarios/run.ts
# One case:
bun run scenarios/run.ts scenarios/03-human-approval-gate.yaml
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

This is deliberately a starter suite. It exercises one real agent per scenario,
including multiple workflow roles on that agent and a human boundary. It does
not yet cover distinct-agent collaboration, operator approval/rejection,
Tasks-backed execution, scheduled dispatch, or delivery fault injection. Those
remain covered by deterministic integration tests, not by these live-LLM cases.

## Recorded smoke run

On 2026-09-12, all three scenarios and the post-run database checks passed using
`openai-codex` / `gpt-5.6-terra`: direct completion (8 iterations), assignment
parameters (11), and the human approval boundary (8). This is one passing smoke
run, not a measured reliability rate. The five verifier unit tests also pass.
