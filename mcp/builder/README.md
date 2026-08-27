# Builder

Builder is the durable orchestration layer for Apteva Helper. An operator states an outcome in the dashboard Build conversation; Helper turns it into a project-scoped goal, an ordered plan, explicit success checks, and a managed-resource inventory. Helper performs platform mutations with its built-in `apteva-server` MCP tools and uses Conversations for progress and approvals.

Builder deliberately has no heartbeat worker. It is re-evaluated when Helper is awakened by the operator, an approval or platform event, or a deliberately scheduled Task.

## Responsibilities

- Builder: goal, plan, step, check, resource, and decision state.
- Helper: reasoning, pacing, execution, and re-planning.
- `apteva-server`: authoritative platform reads and mutations.
- Conversations: operator stream and approval cards.
- Tasks: optional future or recurring wake-ups when the goal actually requires them.

## Helper attachment

Builder is a global app and requires Conversations. After the sidecar returns from `OnMount`, it performs a bounded retry of `AgentToolsAPI().EnsureAppToolsAttached` for `platform_helper`, including the exact bound Conversations installation. The dashboard also calls `POST /setup/reconcile` when Build opens, which handles installations that predate Helper activation without adding a recurring worker.

## Local verification

The workspace `go.work` overlays the local `app-sdk` during development:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Run these commands from `apps/mcp/builder`.

Release verification should also run with `GOWORK=off` so a source install outside this workspace is proven against the published SDK dependency.
