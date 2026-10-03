# Conversations tests

Use `apteva test` for every tier, from this directory, with the pinned Go SDK:

```sh
GOWORK=off apteva test --tier 1 .
GOWORK=off apteva test --tier 2 .
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-5.6-terra scenarios
# Or all checks, retaining failures from earlier tiers:
GOWORK=off apteva test --tier all --provider openai-codex --model gpt-5.6-terra scenarios
```

This suite requires the CLI runner with `setup.driver`, `setup.apps`,
`setup.agents`, and `apteva.test.yaml` support. Build the CLI checkout with
`go build -o /tmp/apteva-test .` in `apteva/` when testing unreleased runner changes.
Use the resulting executable in place of `apteva` above. Core is reused unchanged.

Tier 1 runs all Go unit tests, the dashboard/exported-chat Bun tests, and the
public composer type checks. Tier 2 runs the Go sidecar integration tests and
Playwright dashboard/exported-chat browser tests. `apteva.test.yaml` adds frontend
commands to the runner's standard Go commands; it does not replace them.

Tier 3 provisions a disposable server/project, the actual local app binaries,
install configuration/bindings, and real model-backed agents. Set
`OPENAI_CODEX_ACCESS_TOKEN` and `OPENAI_CODEX_ACCOUNT_ID` in your environment or
private `~/.apteva/test.env`. Never commit credentials. `APTEVA_SERVER_BIN` and
`APTEVA_CORE_BIN` can select existing server/Core binaries. The runner also offers
`--server` for using server-side provider configuration; use a dedicated test
server, since these workflows deliberately create data and restart test agents.

Every YAML file is one native runner scenario. The scenario driver is an argv
command run without a shell. Its Go client workflow receives the runner-owned
agent IDs, install IDs, project, URL and owner credential through environment
variables. It fails when runner resources are missing and does not reuse a personal
agent or provision another platform. The runner independently collects real
telemetry, checks YAML trajectory assertions/token budgets and removes resources.
Drivers preserve HTTP/SSE, retry, approval-card, public-audience, negative-space,
and restart/profile assertions that need several interactions during an LLM run.
Do not invoke the `scenario` Go build tag directly.

## Live coverage

| Scenario | Preserved outcomes |
| --- | --- |
| Chat round trip | Real reply persisted in the originating chat |
| Single conversation | Latest lead-agent lookup and focused chat reply |
| Soft break | Live SSE call ID; targeted break consumed; retry is idempotent |
| Image → Storage → Tickets | Acknowledgement before visible tool work; actual colour recognition; unique ticket; exact Storage ID in message, tool args and ticket; retry deduplication |
| Two conversations | Neither chat receives the other's response |
| Alert | Agent-created, titled operator conversation and warning/error inbox card |
| Approval round trip | Advertised action accepted, card updated in place, verdict acknowledged |
| Report | Same report present in inbox and transcript |
| Public escalation | Visitor reply with no inbox leakage; escalation in operator chat |
| Public self-service | Reply without escalating during a settle period |
| Public refusal | Reply without escalating during a settle period |
| Room fan-out | Two runner-owned agents reply; duplicate submission reuses one row |
| Room messages and mentions | Normal messages/follow-ups reach the lead; named mentions reach only that participant; shared history is readable; `@all` reaches both; explicit recipients override mentions; retry deduplicates; each round checks final replies and a 12-second quiet window |
| Bound approval | Approval/verdict remain in the originating chat |
| Ownership | Unchanged main/chat profiles, no mutation/delegation attempts, history recall and restart/resume |

The image scenario declares Storage copying in `setup.app.config`, enables the
optional Storage binding, and installs Tickets via `setup.apps` with the same
Storage binding. Tickets is a test peer, not a production dependency of
Conversations. `spawnable: true` makes the selected apps available to the
app-owned conversation thread. No preconfigured live install or Tickets agent
is required.

The image test uses the uploaded-attachment path used by the composer. It does
not prove that legacy `data_url`-only submissions without an attachment ID are
mirrored. The config toggle remains required; a Storage binding alone does not
enable copying.

To run one workflow, pass its YAML file instead of the directory. For repeated
LLM evaluations, set `runs` and `required_pass_rate` in that scenario. Results
include driver output, actual tool calls, iterations and token totals with
`--json`; failures also retain artifacts under `--artifacts-dir`. A zero cost
reported for Codex subscription usage does not mean zero model usage.

For a two-agent conversation with ordinary follow-ups and typed mentions:

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol --json scenarios/16-room-messages-and-mentions.yaml
```

This checks durable messages, routing, thread ownership, shared history, and
duplicate replies. It does not exercise browser rendering, the mention picker,
or simultaneous approvals. The runner verifies the requested model against
actual `llm.start` telemetry.

For the native image handoff regression, run the dedicated opt-in scenario with
the real Codex provider and GPT-6.1 Sol:

```sh
APTEVA_RUN_CONVERSATIONS_NATIVE_IMAGE_TIER3=1 \
  GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/17-native-image-to-conversation.yaml
```

The scenario enables Core's provider-level `image_generation` capability for
that disposable agent. It verifies that the generated `blobref://` survives the
native image turn, is passed unchanged to `conversations_send`, remains in a
reloaded transcript, resolves through the authenticated conversation attachment
route as the original PNG, and cannot be fetched through another conversation.
It is intentionally separate because it consumes hosted image-generation
usage; ordinary Tier 3 runs do not enable the capability.

Latest local validation (2026-10-02): one run passed all eight rounds with two
`openai-codex` / `gpt-6.1-sol` agents. Runtime telemetry confirmed the provider
and model. Elapsed time was 179.3 seconds, with 23 iterations and 232,143 total
tokens. Each intended recipient produced exactly one final reply per round;
no untargeted replies or duplicate finals appeared during the 12-second settle
windows. The test used a disposable local server/project and the current local
Conversations source. The Go unit suite also passed. This is one successful
live run, not a measured reliability rate across repeated runs.

Old `go test -tags live` commands and manual install/agent bootstrap scripts are
superseded. Historical release validation documents retain their original run
results; they are not instructions for the current suite.
