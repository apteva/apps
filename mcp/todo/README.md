# Todo

Personal todos with lists, list groups, tags, due dates and recurrence.

## Home widget

Add **Todo list** from Home's widget picker. Each copy has its own settings:

- **List or group:** select an active list or group by name.
- **Sort by:** due date (undated tasks last) or priority (P1 first).
- **Visible todos:** 1–20 rows, with the total remaining count shown separately.

The widget supports half and full width. It shows all open tasks in the chosen
area, including snoozed tasks. Groups show tasks from their active member lists.
Archived lists and groups are excluded; an unavailable selection asks the user to
configure the widget again rather than switching to a broader view.

Complete a task using its check button. Recurring tasks advance to their next
occurrence and stay open, following the full Todo page's behavior. Quick-add
creates a task from the entered title directly in the selected list. For a group,
choose a member list before adding. **Open in Todo** opens the same list or group.
Changes refresh through the app event bus, with a periodic refresh as a fallback.

## Validation

From the apps repository root:

```sh
bun install --frozen-lockfile
bun test mcp/todo/ui
bun run scripts/build-panels.ts --app todo
```

From `mcp/todo` in a standalone checkout or worktree:

```sh
GOWORK=off go test ./...
```
