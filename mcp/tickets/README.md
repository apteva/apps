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

## Development

```sh
go test ./...
go test -tags integration ./...
bun build ui/TicketsPanel.tsx --target=browser --format=esm --minify \
  --external react --external react/jsx-runtime \
  --outfile ui/TicketsPanel.mjs --sourcemap=linked
```

The sidecar uses the standard SDK environment and serves its authenticated API
under `/api/apps/tickets`. The client portal is intentionally token-authenticated
and is exposed by the SDK through the `NoAuth` `/p/` route; possession of an
unguessable intake or ticket token is the authorization boundary.
