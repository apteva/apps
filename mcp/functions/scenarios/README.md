# Functions scenarios

Tier-3 scenarios run by `apteva test` against a live agent. Each
scenario boots the functions sidecar, hands the LLM a directive, and
asserts on observable side-effects (tools called, REST results).

| Scenario | What it covers |
|---|---|
| `01-create-and-invoke.yaml` | create an inline `bun` function, then `functions_invoke` it |
| `02-create-update-delete.yaml` | full lifecycle: create → update (disable) → delete |
