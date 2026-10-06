# Tickets

Tickets is Apteva's repository-independent client feedback and support app.
It owns the external request lifecycle while optional sibling apps enrich it:

- CRM links requesters and mirrors concise activity into the contact timeline.
- Storage keeps screenshots and attachments.
- Tasks and Code remain internal execution targets linked after triage.
- Channels and Workflows can react to the published ticket events.

CRM, Storage, Tasks, Code, and Channels are all optional. With no bindings,
Tickets still provides ticket CRUD, configurable areas, comments, internal
notes, status history, and secure public intake/ticket links.

The project panel includes both list and Kanban board views. Moving a card
between workflow columns updates its status through the normal ticket API, so
the transition remains part of the permanent ticket history.

## Live dashboard updates

The list, Kanban board, and open ticket drawer subscribe to project-scoped
`ticket.*` app-bus events, filtered to the selected installation. Event bursts
are combined and refreshes wait for local saves/uploads. Incoming changes
refresh untouched fields while preserving unsaved edits and reply text.
The panel shares the dashboard SSE connection, with a resumable standalone
fallback, and refreshes when the tab becomes visible or focused again.
The token-authenticated client portal does not subscribe to the private project bus.

## Development

```sh
GOWORK=off go test ./...
GOWORK=off go test -tags integration ./...
bun test ui/live.test.ts
bun run ../../../scripts/build-panels.ts --app tickets
```

The sidecar uses the standard SDK environment and serves its authenticated API
under `/api/apps/tickets`. The client portal is intentionally token-authenticated
and is exposed by the SDK through the `NoAuth` `/p/` route; possession of an
unguessable intake or ticket token is the authorization boundary.
