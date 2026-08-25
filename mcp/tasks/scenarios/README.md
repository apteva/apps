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
| `03-bounded-lookup-no-task.yaml` | A named request thread answers one bounded fact without manufacturing a task. |
| `04-thread-one-time-schedule.yaml` | A named request thread creates one future task owned by the default durable thread and does not execute it early. |
| `05-thread-recurring-schedule.yaml` | A named request thread creates one recurring schedule without setup work, delegation, or early execution. |
| `06-scheduled-execution-and-thread-receipt.yaml` | Due work executes on the default thread and Tasks returns its structured terminal receipt to the opaque creator thread. |
| `07-cross-thread-inventory.yaml` | A named thread lists the agent-wide Tasks inventory directly across creator-thread boundaries. |
| `08-thread-multisource-task.yaml` | A named request thread uses three fake domain reads and creates and completes one task without task-oriented wording. |
| `10-simultaneous-directive-wake-and-task.yaml` | A directive-owned timer and a due Tasks event race on main; both outcomes must complete exactly once. |
| `11-delegated-multistage-progress.yaml` | A four-message delegated occurrence stays running while its Core-spawned worker records meaningful progress milestones. |
| `12-concurrent-multitask-workers.yaml` | Four tasks and four Core workers race assignments and reports without crossing progress, executors, or results. |
| `13-authoritative-worker-task-fetch.yaml` | A general read-only worker fetches execution-critical values from the authoritative task before acting, with no corrective parent handoff. |
| `14-edit-recurring-definition.yaml` | A paused recurring task is renamed and its description, interval, and timezone are edited atomically without resuming it or creating a replacement. |

Scenario 13 also mounts a spawnable in-process `creator-sandbox` MCP. Its
`verify_post` tool performs no external action; the assertion requires the
worker to populate every argument from `tasks_get` before invoking it. This
keeps the handoff test representative of a real Tasks + domain-tool worker
without depending on Patreon, Computer, or network access.

Request scenarios use `setup.interaction: thread`. The runner creates the named
Core thread with its explicit MCP profile and queues `prompt` atomically as its
first event. They do not mount Channels or call a user-facing messaging API.
`directive` remains the durable main-thread role; `setup.thread.directive`
defines the request thread. Runtime placeholders expose the opaque IDs:

- `${APTEVA_TEST_THREAD_ID}`
- `${APTEVA_TEST_DEFAULT_THREAD_ID}`
- `${APTEVA_TEST_AGENT_ID}`
- `${APTEVA_TEST_WAKE_AT}` when `setup.initial_wake` is configured
