# Todo

Personal todos with lists, list groups, tags, due dates and recurrence.

## Home widget

Add **Todo list** from Home's widget picker. It immediately shows **Today + overdue**
across **all lists and groups**, including dated Inbox tasks. No area selection is
required. It uses the same viewer timezone and snooze behavior as Todo's Today view.

Use the filters inside the widget to select a list or group, switch between
Today + overdue, Overdue, Upcoming and All open, or sort by due date or priority.
List names appear alongside tasks when multiple lists are shown. Settings choose
starting filters and 1–20 visible rows (default 8); the remaining count covers the
entire filtered selection. Each copy has independent filters and supports half
and full width. Archived areas are excluded.

Complete tasks using their check buttons. Recurring tasks advance to their next
occurrence and remain open. Quick-add uses the selected list, a chosen member list
for a group, or Inbox by default when viewing all areas. New undated tasks appear
in All open. **Open in Todo** preserves the current area and view. App events and a
periodic fallback refresh the widget.

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
