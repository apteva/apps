# Builder

Builder is the durable orchestration layer for Apteva Helper. An operator states an outcome in the dashboard Build conversation; Helper turns it into a project-scoped goal, an ordered plan, explicit success checks, and a managed-resource inventory. Each goal can stay `build_only`, run a bounded `simulated` campaign, or use `continuous` validation after workflow changes. Helper performs platform mutations with its built-in `apteva-server` MCP tools and uses Conversations for progress and approvals.

The Builder panel is the operator workspace: it embeds the Helper's Conversations stream beside live Builder state for the selected project. The tracker shows the current outcome, validation policy, ordered plan, approval state, success checks, managed resources, and meaningful activity. Builder also contributes the same workspace to the `dashboard.build` slot so the dashboard can mount it without owning app-specific UI.

Builder deliberately has no heartbeat worker. It is re-evaluated when Helper is awakened by the operator, an approval or platform event, or a deliberately scheduled Task.

## Responsibilities

- Builder: goal, plan, step, check, resource, and decision state.
- Helper: reasoning, pacing, execution, and re-planning.
- `apteva-server`: authoritative platform reads and mutations.
- Conversations: operator stream and approval cards.
- Environments: optional isolated runtimes, fake integrations, fixtures, and synthetic state.
- Evals: optional suites, experiments, assertions, evidence, and regression detection.
- Tasks: optional future or recurring wake-ups when the goal actually requires them.

Evals and Environments are intentionally not unconditional Builder dependencies. Helper installs them project-by-project only after the operator opts into simulated or continuous validation. Builder then adds a reserved validation completion check so the goal cannot claim completion until authoritative virtual-world evidence passes.

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
