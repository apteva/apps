# Calendar scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario.

## Run

```bash
apteva test ./scenarios/                                 # all
apteva test ./scenarios/01-create-and-list.yaml -v       # one, verbose
apteva test ./scenarios/ --max-budget-usd 0.50           # spend cap
```

## Scenarios in this directory

| File | What it exercises |
|---|---|
| `01-create-and-list.yaml` | Create a calendar, add three one-off events, list them back. The flagship CRUD loop. |
| `02-find-slot.yaml` | Build a partially-booked day, then find an open 30-min window. Validates events_find_slot. |
| `03-holidays.yaml` | Bulk-load French holidays for a year. Validates the holidays_set helper + auto-created calendar. |
