# Proposals

A project-scoped Apteva sidecar for client proposals. Create and preview a draft in the Proposals dashboard panel, publish a revision, and share its private URL. You can duplicate an existing proposal to reuse its structure.

Clients can include, defer, or decline optional items. Categories support any number of options, at most one, or exactly one for acceptance. Required lines are included automatically. The client can accept the selection, decline the proposal, or request changes with a note. A published revision freezes its prices and scope, and the server calculates the recorded total. Publishing a replacement closes the previous open link.

## Integrations

- CRM is optional. Import a deal by opportunity ID and use **Sync accepted value to CRM** to update its amount and currency after acceptance.
- Messaging is optional. Set a verified default email sender in the same project to send invitations and reminders. Manual link sharing works without it.
- Automations can consume `proposal.published`, `proposal.accepted`, `proposal.declined`, `proposal.changes_requested`, and `proposal.withdrawn`. A project-scoped subscriber can use `proposals_acceptance_get` to retrieve the accepted lines. Acceptance is recorded by Proposals. Signing and invoicing are separate downstream operations.

The app uses the platform's public URL for client links. Set `public_base_url` in app settings to override it with an HTTPS app base such as `https://example.com/api/apps/proposals`. A link grants access to its revision. Rotate it to revoke a shared link.

## Development

From `mcp/proposals`, use `GOWORK=off go test ./...` to test the pinned SDK and `GOWORK=off go build -o /tmp/apteva-proposals .` to build the sidecar. From the apps repository root, rebuild the dashboard bundle with `bun run scripts/build-panels.ts --app proposals`.
