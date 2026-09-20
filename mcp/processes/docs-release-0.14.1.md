# Processes 0.14.1

Processes is native-only. The Tasks app is no longer a dependency or an
execution backend, and Processes no longer exposes standalone task tools or
task-specific HTTP routes. Procedure runs and step runs are the single source
of truth for state, progress, approvals, evidence, retries, and history.

This patch also hardens Process worker delivery. Persistent sequential workers
receive `step_claim`, `step_get`, `step_update`, and run-control tools.
Independently dispatched workers receive `step_get`, `step_update`, and
run-control tools, and are explicitly told not to call the sequential-only
claim operation.

Existing historical rows remain readable. The 010 migration normalizes legacy
assignments for future native execution and changes new step events to the
`step.*` event namespace without deleting audit history.

Evals is an optional dependency (`>=0.5.9`) for isolated testing of immutable
procedure revisions. Environments remains owned by Evals rather than becoming a
direct Processes dependency.

This release changes Processes only. It does not modify Conversations.
