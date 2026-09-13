# Assignment event triggers

Processes can listen to an installed app's events and start an assignment. It
receives the business event itself; agents receive their individual work steps
from the existing run engine. Neither Tasks nor Workflows is required.

In an assignment, open **Event triggers**, select **Add app event**, choose the
source installation and event, add conditions, and map event fields to declared
process parameters. Save it paused, preview a sample event, then activate it.
The process and assignment must also be active. `sync_pending=false` and
`subscription_enabled=true` confirm that the platform listener is installed.

All conditions must match. Field paths include `data.customer_id`, `topic`,
`source_app`, `source_install_id`, `project_id`, and `event_id`. Conditions support
`eq`, `neq`, `contains`, `exists`, `gt`, `gte`, `lt`, and `lte` without evaluating
arbitrary code. Mappings preserve JSON types and must name declared parameters.
For example, map `customer_id` to `data.id`. Missing fields or invalid types are
recorded as failed events; they never create a run. Event payloads remain data,
not instructions or additional authority for the agent.

An unscheduled assignment may leave required values for event mappings: create
its paused trigger before activating the assignment. Scheduled assignments still
need complete saved parameter values. Manual starts validate their complete
parameters independently. Existing schedules are unchanged.

**Preview only** never creates work. **Start test run** is an explicit action
that creates real work from a sample. Its key is retained on request failure so
retrying does not create another run. **Event history** shows saved inputs,
trigger revisions, matched/skipped/failed outcomes, and the resulting run.
Failed events can be retried explicitly against current trigger rules; the
original failure remains in history and the new record references it via
`retry_of`. Successful events cannot be replayed through this action.

Pausing a trigger or its assignment stops new starts, not existing runs. Platform
subscriptions reconcile within five seconds; Processes also checks current
status when accepting delivery. Events published while unsubscribed are not
backfilled on reactivation. Events queued for an older listener revision are
canceled or recorded as skipped, preventing old work from using new rules.
Parent status changes invalidate queued deliveries atomically, including a
pause and resume that both occur before the next subscription reconciliation.

## Delivery and storage

Requires the SDK EventBusClient APIs and the platform app-target subscription
outbox. Processes declares `platform.events.subscribe`. The platform binds the
subscriber to its own installation and checks the source installation and
project. Each trigger selects a specific source install, not an app-name
firehose. The source picker uses manifests' `provides.publishes` catalog.

The platform stores matching app deliveries in the same transaction as agent
subscription deliveries before acknowledging publication. Delivery is retried
with capped backoff while the destination is offline, across dispatcher restarts.
Processes atomically stores the receipt, trigger snapshot, mapped parameters,
and reserved run before acknowledging. Existing workers deliver the run after
that commit and recover undispatched work after restart.

Sources should keep their own transactional outbox and call
`EventBusAPI().PublishAppEvent(projectID, stableEventID, topic, payload)`, reusing
the ID on retry. The platform rejects the same ID with changed content. Older
`Emit` callers still work, but separate publishes without a stable ID are
separate events. Transport retries of one accepted event remain deduplicated.
A stable publication key is scoped to its source installation.

The app stores `process_triggers` and `process_trigger_events`. Each event-started
run has `trigger_event_id`. History retains received payloads for audit; it is
project-scoped. The history API returns the newest 100 records per trigger.

## MCP / HTTP

MCP: `trigger_sources`, `triggers`, `trigger_get`, `trigger_create`,
`trigger_update`, `trigger_activate`, `trigger_pause`, `trigger_preview`,
`trigger_test_run`, `trigger_events`, `trigger_event_retry` (prefixed
`processes_` on the agent surface). Updates and activation/pause require
`expected_revision`. Configuration edits require a paused trigger.

HTTP under the Processes `/processes` prefix:

- `GET /trigger-sources`
- `GET|POST /{process}/assignments/{assignment}/triggers`
- `GET|PUT /{process}/triggers/{trigger}`
- `POST /{process}/triggers/{trigger}/{activate|pause|preview|test_run|event_retry}`
- `GET /{process}/triggers/{trigger}/events`

All routes require authenticated project context. Preview/test bodies use an
`event` object with `topic` and `data`. Test runs and failed-event retries require
an `idempotency_key`; retries also take `event_record_id`.
