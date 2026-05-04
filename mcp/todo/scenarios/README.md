# todo · scenarios

End-to-end smoke tests for the personal todo app, run against a real
LLM via the standard scenario harness (same one calendar/tasks use).

| File | What it exercises |
|------|-------------------|
| `01-quick-add.yaml`         | NL parser via `todos_quick_add` (priority, due hints, project, tag) |
| `02-today-and-complete.yaml`| Triage loop: `todos_list` → pick by priority → `todos_complete` |

Run a single scenario:

```sh
go test ./... -run TestScenarios/quick-add
```

Run the whole suite (matches the convention in `mcp/calendar`):

```sh
go test ./...
```
