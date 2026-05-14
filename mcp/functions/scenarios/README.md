# Functions scenarios

Tier-3 scenarios run by `apteva test` against a live agent. Each
scenario boots the functions sidecar, hands the LLM a directive, and
asserts on observable side-effects (tools called, REST results).

| Scenario | What it covers |
|---|---|
| `01-create-and-invoke.yaml` | create a node function (deploys v1), then `functions_invoke` it against a warm worker |
| `02-deploy-and-rollback.yaml` | immutable-version lifecycle: create → `functions_deploy` v2 → `functions_rollback` to v1 |
