# Billing scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario; the runner installs the local
billing build, gives the agent the directive, watches telemetry, then
runs assertions against the running sidecar's REST surface.

## Run

```bash
# Spawn a clean apteva-server in a temp dir and run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-draft-and-finalize.yaml -v

# Use an already-running server (skip spawn).
apteva test ./scenarios/ --server localhost:5280

# Hard budget across scenarios.
apteva test ./scenarios/ --max-budget-usd 1.50
```

## What each scenario covers

| File | Flow | Why it matters |
|---|---|---|
| `01-draft-and-finalize.yaml` | upsert → create → finalize | The bread-and-butter "make me an invoice" path. If this breaks, billing is unusable. |
| `02-record-wire-payment.yaml` | finalize → payments_record(wire) → paid | Verifies the agent picks `payments_record` (not Stripe, not anything else) for non-card payments. |
| `03-customer-balance.yaml` | `customers_get_context` answers "what's outstanding" | Pre-flight read works; agent doesn't over-call by looping search + list. |
| `04-void-confirmation.yaml` | void requires explicit confirmation | The skill doc says quote-back-and-confirm. Cautious-mode test pins this behavior. |
| `05-render-pdf.yaml` | finalize → `invoices_render_pdf` returns base64 + filename | Agent picks `invoices_render_pdf` (not the `/print` HTML view) when the user wants bytes for delivery. |

## Authoring guidelines

- **Assert outcomes via REST, not tool-call paths.** LLMs vary; pin
  the resulting state ("1 invoice with status=paid") rather than the
  exact tool sequence.
- **End with "Then stop."** so the agent doesn't keep iterating after
  the test goal.
- **Cautious mode (`mode: cautious`) for confirmation tests.** It's
  the only mode where the user is a turn-by-turn participant — the
  void-confirmation scenario relies on it.
- **Budgets are ceilings, not targets.** Set ~50% above the best
  observed run; LLM jitter shouldn't fail the suite.
- **One assertion per claim.** `expect_count` for "the right number
  of rows landed", `tool_called` for "the agent reached for the
  right primitive", `agent_response_contains` for "the agent said
  the words it was supposed to."

## Adding a new scenario

1. Pick a single user-shaped flow you want to pin.
2. Front-load any setup steps in the directive — the agent does them
   too, exercising the same code paths the real user would.
3. Make assertions about the resulting REST state. Avoid asserting
   on internal DB columns the public API doesn't expose.
4. Run `apteva test ./scenarios/your-file.yaml -v` until it passes
   3 times in a row before checking in. LLM flakes show up in
   small-N runs.
