# Games scenarios

Tier 3 live-agent tests — a real apteva-core is spawned and a real LLM
calls the tools. Each YAML file is one scenario; the runner installs the
local Games build (and its Auth dependency), hands the agent the
directive, watches telemetry, then asserts against the sidecar's
/admin REST surface.

```bash
apteva test ./scenarios/                 # every scenario in a temp server
apteva test ./scenarios/01-define-progression.yaml -v
apteva test ./scenarios/ --server localhost:5280
apteva test ./scenarios/ --max-budget-usd 0.50
```

The format is the same as the CRM scenarios: `setup.app.path` points at
this directory, `directive` is the free-text instruction, `assert`
checks tool calls and HTTP responses, `budget` caps tokens and cost.
