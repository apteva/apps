# Processes 0.6.0

Processes now supports native one-off tasks alongside procedure tasks. Create
standalone work without a process or run, or add required or optional tasks to an
active run. The Tasks app remains optional.

- A unified **Work** panel lists procedure tasks, standalone work, and added run
  tasks, with assignee, status, origin, process, search, and overdue filters.
- Create work or approval tasks with instructions, expected results, an agent or
  human project operator, and a due date. Ready agent work dispatches immediately;
  the due date is a deadline, not a scheduled start.
- A run's task workspace supports required work that gates completion and optional
  work that can continue after successful run completion.
- Six MCP tools expose the same model: `tasks`, `task_runs`, `task_create`,
  `task_get`, `task_update`, and `task_cancel`, under the normal `processes_` prefix.
- Native work reuses existing step storage, dependency checks, tracked agent
  dispatch and retries, approval validation, and audit history. Migration preserves
  existing step IDs and outcomes; existing step APIs continue to work.
- Creation is idempotent and updates check revisions. Only the assigned executor
  reports outcomes. Completion needs evidence; approvals need an explicit decision.

## Compatibility

This release changes Processes only; it introduces no Tasks app or server changes.
The minimum platform remains Apteva 0.51.3 and app-sdk stays pinned to v0.80.0.
Event triggers retain the durable app subscription requirement from v0.5.0.
Publishing this release does not upgrade the platform or installed apps.

Human assignees are authorized project operators, not named users. Tasks-backed
procedure steps still report outcomes through their linked Tasks records. Legacy
unstructured Tasks-backed runs cannot accept added native tasks; use native
execution or structured steps. Reassignment and text changes are locked once
execution may have started. Cancellation cannot revoke external actions already
performed or work already dispatched.

## Validation

The native task implementation passed the full Go race and real-sidecar
integration suites, migration/authorization/idempotency/completion tests, 12 React
panel and Work tests, strict TypeScript, and panel build/import checks.
Desktop/mobile browser checks with a mock API covered standalone creation and
completion, required run attachment, and cancellation, with no browser errors or
horizontal viewport overflow. Release validation also runs the combined Go race
and integration suite and a standalone Linux build from the release source.
No real LLM test was run for the native task feature.

See [native task documentation](docs-native-tasks.md) for APIs, permissions,
required/optional behavior, and integration limits.
