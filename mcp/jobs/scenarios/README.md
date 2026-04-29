# Jobs scenarios

Tier-3 scenarios run by `apteva test` against a live agent. Each
scenario boots the jobs sidecar, hands the LLM a directive, and
asserts on observable side-effects (tools called, REST results).

| Scenario | What it covers |
|---|---|
| `01-schedule-reminder.yaml` | cron schedule + event target with `instance_id: "self"` |
| `02-schedule-and-cancel.yaml` | schedule → list → cancel in a single session |
