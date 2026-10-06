# CRM v0.9.10

Makes `conversations_inbox` eligible for strict read-only tool discovery by
publishing standard MCP annotations: `readOnlyHint: true` and
`destructiveHint: false`.

The app pins App SDK v0.91.0, which emits these annotations in `tools/list`
without changing legacy tools or app-only exposure. Regression tests check
both CRM's tool declaration and its real sidecar MCP response, and ensure
mutation tools are not marked read-only.

No database migrations, inbox behavior changes, data edits, or Core discovery
heuristics are required. All previous CRM fixes remain included.
