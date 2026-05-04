# Auth scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool calls. Each YAML file is one scenario; the runner installs the local auth build, gives the agent the directive, watches telemetry, then runs assertions against the running sidecar's REST surface and MCP tools.

## Run

```bash
# Spawn a clean apteva-server in a temp dir and run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-register-client.yaml -v

# Use an already-running server (skip spawn).
apteva test ./scenarios/ --server localhost:5280

# Hard budget across all scenarios.
apteva test ./scenarios/ --max-budget-usd 0.50
```

## What each scenario covers

| File | Surface under test | Why it matters |
|---|---|---|
| 01-register-client.yaml | `auth_clients_create` | The first thing any agent operating an auth install does |
| 02-disable-spam-user.yaml | `auth_users_search` + `auth_users_disable` | Common ops task — agent finds a user by email and shuts them down |
| 03-revoke-sessions.yaml | `auth_users_revoke_sessions` after a credential leak | Incident-response shape: someone's password was leaked, kill all sessions |
| 04-audit-trail-investigation.yaml | `auth_audit_search` + `auth_users_get_context` | Read-only — agent investigates a suspicious login pattern |
| 05-rotate-client-secret.yaml | `auth_clients_rotate_secret` | Periodic hygiene — agent rotates a client's secret on demand |

## Authoring guidelines

- **Assert outcomes, not paths.** Don't pin "agent emits exactly these 3 tool calls in this order" — LLMs vary. Pin "the user with email X is now status=disabled" via the REST surface or `auth_users_get` MCP call.
- **End with "Then stop."** in the directive — keeps the agent from looping after the test goal is reached.
- **Budgets are ceilings, not targets.** Set them ~50% above your best observed run so flaky LLM rounds don't fail the suite.
- **Seed via REST or MCP, not via agent.** Each scenario `setup` block can pre-create users / clients so the agent can focus on the operation under test instead of wasting tokens on bootstrap.
