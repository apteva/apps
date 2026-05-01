# Status scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML is one scenario the runner installs + executes.

## Run

```bash
apteva test ./scenarios/                              # all
apteva test ./scenarios/01-set-and-clear.yaml -v      # one, verbose
apteva test ./scenarios/ --max-budget-usd 0.20        # spend cap
```

## Scenarios in this directory

| File | What it exercises |
|---|---|
| `01-set-and-clear.yaml` | The full set → get → clear lifecycle. |
| `02-tone-discipline.yaml` | Multi-stage status updates with the right tone. Validates upsert semantics + tone enum. |
