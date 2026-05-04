# health · scenarios

End-to-end smoke tests for the health app, run against a real LLM
via the standard scenario harness.

| File | What it exercises |
|------|-------------------|
| `01-log-and-list.yaml`         | NL parser via `health_log` (weight, sleep_hours, bp_systolic/diastolic) + `metrics_kinds` |
| `02-workout-and-summary.yaml`  | Workout parsing (`run · 5km · 26min`) + `health_summary` markdown output |

Run a single scenario:

```sh
go test ./... -run TestScenarios/log-and-list
```

Run the whole suite:

```sh
go test ./...
```
