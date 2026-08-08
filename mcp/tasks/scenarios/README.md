# Tasks scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario; the runner installs the local
tasks build, gives the agent the directive, watches telemetry, then
runs assertions against the running sidecar's REST surface.

## Run

```bash
# Spawn a clean apteva-server in a temp dir and run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-create-and-complete.yaml -v

# Use an already-running server (skip spawn).
apteva test ./scenarios/ --server localhost:5280

# Hard budget across scenarios.
apteva test ./scenarios/ --max-budget-usd 0.50
```

## Scenarios in this directory

| File | What it exercises |
|---|---|
| `01-create-and-complete.yaml` | One opaque-thread-owned task moves through create → progress → complete without duplication. |
| `02-list-and-update.yaml` | One recurring task is created, listed directly from Tasks, and paused without a setup-task duplicate. |
