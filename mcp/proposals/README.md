# Proposals

A project-scoped Apteva sidecar for client proposals. Create and preview a draft in the Proposals dashboard panel, publish a revision, and share its private URL. You can duplicate an existing proposal to reuse its structure.

Clients use visible **Include**, **For later**, and **Decline** buttons for optional items. Multi-select categories also offer group buttons to include, defer, or decline every optional item at once, with live group subtotals. Required lines always stay included, and choose-one categories still allow only one included item. Clicking an active choice clears it. Categories support any number of options, at most one, or exactly one for acceptance. Required lines are included automatically. The client can accept the selection, decline the proposal, or request changes with a note. A published revision freezes its prices and scope, and the server calculates the recorded total. Publishing a replacement closes the previous open link.

## Proposal language

Choose **English (Default)** or **Français** in each draft’s **Proposal details**. This controls the public page, client preview, response buttons and messages, invitation emails, and date/price formatting. Write the title, overview, categories, item descriptions, and terms in that language; content is not automatically translated. The MCP content field is `language: "en" | "fr"`; omitted values and older proposals default to English. Language is frozen with the published revision.

## Client backlog

Use the **Client backlog** tab to create clients and keep private ideas, categories, scope descriptions, optional estimates, and internal notes. When CRM is linked, search contacts by name, email, or company and select a result to fill the client details. Items without a price can be planned now and priced later.

Select priced items and choose **Create proposal from selection**, or select a saved client in an existing draft and use **Add from client backlog** under **Scope & pricing**. The draft receives copies of only the selected scope and prices. Unselected items and internal notes stay in the backlog. Later backlog edits do not change those copies. Archive items when they are no longer needed.

Clients and their backlog use separate, client-scoped MCP permissions. Older proposals keep working with their existing client details.

## Integrations

- CRM is optional. Import a deal by opportunity ID and use **Sync accepted value to CRM** to update its amount and currency after acceptance.
- Messaging is optional. Set a verified default email sender in the same project to send invitations and reminders. Manual link sharing works without it.
- Automations can consume `proposal.published`, `proposal.accepted`, `proposal.declined`, `proposal.changes_requested`, and `proposal.withdrawn`. A project-scoped subscriber can use `proposals_acceptance_get` to retrieve the accepted lines. Acceptance is recorded by Proposals. Signing and invoicing are separate downstream operations.

The app uses the platform's public URL for client links. Set `public_base_url` in app settings to override it with an HTTPS app base such as `https://example.com/api/apps/proposals`. A link grants access to its revision. Rotate it to revoke a shared link.

## Development

From `mcp/proposals`, use `GOWORK=off go test ./...` to test the pinned SDK and `GOWORK=off go build -o /tmp/apteva-proposals .` to build the sidecar. From the apps repository root, rebuild the public page with `bun build mcp/proposals/ui/offer-source.js --target browser --outfile mcp/proposals/ui/offer.js`, then rebuild the dashboard bundle with `bun run scripts/build-panels.ts --app proposals`. Run the scope and localization tests with `bun test mcp/proposals/ui/offer.test.ts`.
