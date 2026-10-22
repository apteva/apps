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
| Short multi-step cadence | Related list and todo lookups complete with one acknowledgement and one final reply, without an unnecessary progress message |
| Long multi-step progress | A meaningful stage boundary produces one intermediate progress update before the final outcome |
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

## Interactive components

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/19-interactive-components.yaml
```

This provisions an isolated agent and tests an optional question followed by a
final reply without answering it, idle activity while the question stays in the
inbox, a later answer delivered to the same thread, saved results, identical
retries and conflicting answers, and a blocking form that resumes after input.
No additional agent tool is introduced. Unit/race checks additionally cover
concurrent card updates, missing/invalid values, skip retries, private access,
archiving, durable delivery recovery, expiry, and inbox opt-out. Browser checks
cover widget/panel submission, mobile widths, reload, inbox clearing and skipping
required fields, alongside the existing approval/report/alert tests.

Local validation on 2026-10-09: the real `openai-codex` / `gpt-6.1-sol` scenario
passed in 113.2 seconds, with 10 iterations and 146,625 total tokens. Actual
telemetry verified the provider/model. This is one successful run, not a
reliability-rate estimate.

The first attempt found a Server subscription startup limitation: app telemetry
caches eligible agents and refreshes them every 60 seconds
(`server/apps_telemetry.go`, `telemetryOwnedAgentsRefresh`). The runner installs
Conversations before creating the agent, so its first turn can occur before the
app receives that agent's `event.received` and completion events. The question
and final reply were saved, but activity remained active. This scenario waits
65 seconds for freshly provisioned agents to enter the subscription before its
first user turn. The Server limitation remains; no Server code was changed.

Final checks for this implementation passed: the full pinned-SDK Go suite,
interactive-component race checks, sidecar integration tests, 106 Bun UI/client
tests, 90 Playwright browser tests, frontend builds, and public consumer type
checks. Older browser assertions were aligned with the already-shipped tool-row
Thinking animation and unread dots. The spacing regression checks caught an
empty component wrapper; it now renders nothing for ordinary messages without
components.

## Thread-menu release validation — 2026-10-14

The panel, browser widget and history drawer share the same thread action menu.
Browser regressions cover right-click without navigation/read acknowledgement,
rename/read targeting, draft preservation, keyboard focus, mobile/viewport-edge
positioning, archive/restore, confirmed deletion, permission errors, details,
host opt-out and custom actions. Existing activity, composer, inbox, attachment,
unread and transcript-spacing regressions remain covered.

Release v0.24.48 passed 106 Bun tests, all 95 Playwright tests, consumer type
checks, the full pinned-SDK Go suite, and the full Go race/integration suite.
A fresh isolated Tier 3 `openai-codex` / `gpt-6.1-sol` run passed in 130.5 seconds
with 10 iterations and 147,422 total tokens, verifying optional and blocking
widget flows again using the current local Server/Core binaries. No live user
agents or production instances were restarted for this validation.

## Suggested next message

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/20-suggested-reply.yaml
```

The scenario uses a disposable project/agent. It asks the real model for one
suggested next user message alongside its final reply, checks durable reload,
no inbox attention, idle activity and a quiet window without automatic work,
then explicitly sends the suggested text as a user turn. It verifies exactly
one continuation despite a send retry and no suggestion after closing the chat.
The paired browser suite tests actual Tab and touch acceptance, proves no send
or action endpoint is called until the user submits, and covers dismissal,
reload, existing drafts/attachments, pending input/sends, active/queued turns,
conversation switching, host opt-out, IME and ordinary keyboard navigation.
These are complementary checks: the Tier 3 driver exercises the model/server
handoff; the browser tests exercise the composer UI.

Local model run on 2026-10-22 passed with actual `openai-codex` / `gpt-6.1-sol`
telemetry: 95.2 seconds, 7 iterations and 102,433 total tokens. The run contains
three conversation sends: the final with one suggestion, the response after
actual user submission, and the closing response. No additional suggestion tool
or generation request was introduced. The runner creates its own isolated
Server/Core processes and leaves existing local and production agents alone.

Final local validation passed: 108 Bun UI/client tests, all 107 Playwright
browser tests (including actual touch/mobile viewport checks), consumer type
checks, packaged UI builds, and `GOWORK=off go test -race -tags=integration ./...`.
The dedicated real-model Tier 3 run above passed separately. These changes are
unreleased; existing release tags and installed app versions are unchanged.
