# Tasks scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario; the runner installs the local
tasks build, gives the agent the directive, watches telemetry, then
runs assertions against the running sidecar's REST surface.

## Run

```bash
# Spawn a clean apteva-server in a temp dir and run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-create-and-complete.yaml -v

# Use an already-running server (skip spawn).
apteva test ./scenarios/ --server localhost:5280

# Hard budget across scenarios.
apteva test ./scenarios/ --max-budget-usd 0.50
```

## Scenarios in this directory

| File | What it exercises |
|---|---|
| `01-create-and-complete.yaml` | One opaque-thread-owned task moves through create → progress → complete without duplication. |
| `02-list-and-update.yaml` | One recurring task is created, listed directly from Tasks, and paused without a setup-task duplicate. |
| `03-brief-chat-no-task.yaml` | A simple saved-conversation reply stays task-free and is delivered exactly once. |
| `04-chat-one-time-schedule.yaml` | Chat creates one future task owned by the default durable thread and does not execute it early. |
| `05-chat-recurring-schedule.yaml` | Chat creates one recurring schedule without setup work, delegation messages, or early execution. |
| `06-scheduled-execution-and-receipt.yaml` | Due work executes on the default thread and its terminal receipt returns one requested result to the creator conversation. |
| `07-chat-lists-agent-inventory.yaml` | Chat lists the agent-wide Tasks inventory directly without a thread-to-thread status query. |
| `08-chat-natural-multisource-task.yaml` | A natural multi-area review creates and completes one conversation-owned task without task-oriented wording. |
| `09-chat-bounded-lookup-no-task.yaml` | A single bounded lookup remains task-free, preserving the other side of the classification boundary. |
| `10-simultaneous-directive-wake-and-task.yaml` | A directive-owned timer and a due Tasks event race on main; both outcomes must complete exactly once. |
| `11-delegated-multistage-progress.yaml` | A four-message delegated occurrence stays running while its Core-spawned worker records meaningful progress milestones. |
| `12-concurrent-multitask-workers.yaml` | Four tasks and four Core workers race assignments and reports without crossing progress, executors, or results. |
| `13-authoritative-worker-task-fetch.yaml` | A general read-only worker fetches execution-critical values from the authoritative task before acting, with no corrective parent handoff. |

Scenario 13 also mounts a spawnable in-process `creator-sandbox` MCP. Its
`verify_post` tool performs no external action; the assertion requires the
worker to populate every argument from `tasks_get` before invoking it. This
keeps the handoff test representative of a real Tasks + domain-tool worker
without depending on Patreon, Computer, or network access.

Conversation scenarios use `setup.interaction: conversation`: `directive` is
the agent's durable role and `prompt` is sent as a real dashboard-channel user
message after the agent starts. Runtime placeholders expose the opaque IDs:

- `${APTEVA_TEST_CONVERSATION_ID}`
- `${APTEVA_TEST_CONVERSATION_THREAD_ID}`
- `${APTEVA_TEST_DEFAULT_THREAD_ID}`
- `${APTEVA_TEST_AGENT_ID}`
- `${APTEVA_TEST_WAKE_AT}` when `setup.initial_wake` is configured
