# Processes changelog

Per-release notes, moved out of the README and the agent skill. Release docs: `docs-release-*.md`.

## Agents write the run summary again (0.20.2)

Every step worker has `processes_summary_update` again; the last step's worker
saves the run summary the run card shows, and a blocked worker saves the
reason. See `docs-release-0.20.2.md`.

## Worker started marks the step running (0.20.1)

Processes moves an isolated step from ready to running when its worker starts,
with a "Worker started" history entry, so the panel shows the step as being
worked on. See `docs-release-0.20.1.md`.

## Simpler agent protocol (0.20.0)

Step workers get one brief holding the step, its inputs and all completed
earlier results, and finish with one `step_update` carrying `outcome`.
Isolated workers have 4 Processes tools (plus 6 only for memory procedures).
Status changes are one tool per object (`status`, `assignment_status`,
`trigger_status`); `assignments` with `assignment_id` replaces `assignment_get`.
Agents see 42 tools instead of 49. See `docs-release-0.20.0.md`.

## Durable wait ownership (0.18.3)

`step_update.wait` records why a worker must remain attached. Save the original
blocker and an exact recoverable output checkpoint with every wait.

| Kind | Ownership and recovery |
| --- | --- |
| `hold` | No live approval, callback, child or native timer remains. Suspend the step and automatically release its worker. Explicit operator recovery is required; no automatic replay. |
| `timer` | New Processes-owned delay. Save an exact RFC3339 `wake_at`; release the worker and resume the same step from its checkpoint at that deadline. |
| `approval` | Retain the owner of the exact pending approval `reference`. |
| `callback` | Retain the owner of the exact callback/subscription `reference`. |
| `thread_timer` | Retain the owner and exact existing native `wake_at` and timer `reference`. Never clear/recreate an armed native timer to migrate it. |

The assigned worker verifies dependency records before surrendering ownership.
A missing reference or an expired-looking message is not evidence that a real
approval/callback/timer has settled. Unknown ownership remains attached.
Non-delegating workers only may use releasable waits. Delegating owners keep
their children and callbacks.

Legacy waiting/blocked steps receive one tracked ownership-revalidation request
per saved revision after ten minutes, in batches of ten per project. The event
uses a stable identity across lost replies and sidecar restarts. The worker
checks the original records and registers its actual dependency or parks a stale
hold. A missing response never authorizes termination. This is a bounded upgrade
handshake, not recurring model polling of every wait.

Checkpoint, ownership, suspension and audit history commit together. A `done`
acknowledgement follows durable retirement reservation. Timer continuations wait
for cleanup acknowledgement, use fresh worker/delivery identities, and preserve
output, progress, evidence and frozen policy. Legacy timers/approvals remain
attached until explicitly classified; they are never expired just by age.


## Unavailable owners and restored workers (0.18.4)

Legacy ownership revalidation continues after an unavailable owner or failed
tracked delivery. Each step/revision has durable retry backoff (one minute,
doubling to fifteen minutes), and accepted requests keep their original stable
event identity. Stopped owners are deferred without waking them or acknowledging
their waits. Deferred failures cannot occupy the next page indefinitely; eligible
pages drain ten at a time. Revalidation IO runs outside the action mutex, so a
worker can immediately save its checkpoint while other deliveries are pending.

Cleanup requires a live owner acknowledgement. Read-only `thread.spawn` metadata
also repairs cleanup receipts predating a worker restore for an already terminal
run. Only an exact app-owned agent/thread/run in the same project is eligible.
Pending retry order and newer acknowledgements remain unchanged; unfinished runs,
approval owners and native timer owners are excluded. Processes requests
`platform.telemetry.observe` for this metadata repair. It does not read telemetry
content. Backfill is paginated per agent (25 events), bounded to the preceding day on first
start, then checks each minute with a one-hour overlap and durable cursor/backoff.

The v0.18.5 repair keeps a separate cursor and retry deadline for each known
cleanup owner, queries one agent at a time, and defers stopped owners without
waking them. This keeps busy-project metadata reads within the host query budget.

In v0.18.6, an accepted legacy inspection without a saved ownership checkpoint
may be requested again after fifteen minutes, at most three accepted requests
per revision. Ambiguous delivery retries keep the same request identity; a new
inspection gets a fresh identity. Known runtime failure holds overwritten with
a running label are repaired only when the exact original runtime audit event,
suspended delivery and no retained dependency all agree. Evidence stays intact
and recovery remains explicit.

In v0.19.0, the panel has a project-wide Assignments tab listing every
assignment with its process, agents, schedule, run counts and last run; it
refreshes from assignment and run events on the app bus. `GET /processes/assignments`
serves it, and `GET /processes/runs` accepts `process_id` (scoped to the project)
alongside `assignment_id`. Assignment cards and the process list name every
agent an assignment uses, and each card shows which agent handles each step.

In v0.19.1, run cards draw their state colour as an inset stripe instead of a
left border, so hover and selection outline every side evenly; waiting,
scheduled, blocked and queued runs get matching stripes.

In v0.19.2, run cards drop the coloured left stripe and tint; the status pill
alone shows each run's state.

In v0.19.3, procedure versions show in the process list, on run cards, on the
project Assignments tab and on assignment cards; pins older than the latest
version name it. `GET /processes/assignments` adds `process_version`.

In v0.19.4, step tool activity keeps a tool result that arrives just after the
step completes when its call was made during the step, so the call that
completes a step no longer stays "running".
