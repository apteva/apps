# CRM scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario; the runner installs the local
CRM build, gives the agent the directive, watches telemetry, then
runs assertions against the running sidecar's REST surface.

## Run

```bash
# Spawn a clean apteva-server in a temp dir and run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-create-contact.yaml -v

# Use an already-running server (skip spawn).
apteva test ./scenarios/ --server localhost:5280

# Hard budget across scenarios.
apteva test ./scenarios/ --max-budget-usd 0.50
```

## Scenario format

```yaml
name: short-id                 # filename used if omitted
description: longer free-text
timeout: 90s                   # per-scenario hard limit
max_iterations: 8              # cap on agent rounds

setup:
  mode: autonomous             # autonomous | cautious | learn
  app:
    path: ../                  # local path to the app under test
    config: { … }              # install-time config (string→string)

directive: |
  Free-text instruction handed to the agent.

assert:
  - tool_called: foo | bar     # at least one of the named tools fired
  - http: GET /api/apps/crm/contacts
    expect_status: 200
    expect_count_at: contacts  # JSON field name to count under
    expect_count: 1
  - iterations_at_most: 8

budget:
  total_tokens: 12000
  cost_usd: 0.20
```

## Authoring guidelines

- **Assert outcomes, not paths.** Don't pin "agent emits exactly these
  3 tool calls in this order" — LLMs vary. Pin "agent created 1
  contact with email X" via the REST surface.
- **End with "Then stop."** in the directive — keeps the agent from
  looping after the test goal is reached.
- **Budgets are ceilings, not targets.** Set them ~50% above your
  best observed run so flaky LLM rounds don't fail the suite.
