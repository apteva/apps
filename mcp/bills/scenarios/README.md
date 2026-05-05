# Bills scenarios

Tier 3 live-agent tests — real apteva-core spawned, real LLM tool
calls. Each YAML file is one scenario; the runner installs the local
bills build, gives the agent the directive, watches telemetry, then
runs assertions against the running sidecar's REST surface.

## Run

```bash
apteva test ./scenarios/
apteva test ./scenarios/02-approve-and-pay.yaml -v
apteva test ./scenarios/ --max-budget-usd 1.50
```

## What each scenario covers

| File | Flow | Why it matters |
|---|---|---|
| `01-log-bill.yaml` | upsert vendor → bills_create | Bread-and-butter "we got a bill" path. If this breaks, AP is unusable. |
| `02-approve-and-pay.yaml` | create → approve → schedule → record payment | Verifies the agent picks the full state-machine sequence (not invoices_finalize from billing). |
| `03-vendor-spend.yaml` | `vendors_get_context` answers "lifetime spend" | Pre-flight read works; agent doesn't loop search + list when one tool gives the answer. |
| `04-reject-vs-void.yaml` | reject (not void) when the bill is wrong | The skill doc's marquee distinction. Cautious-mode test pins it. |
| `05-attach-existing-pdf.yaml` | upload to storage → `bills_attach_file` to link | Two-step composition path; the agent should reach for the linker, not re-upload. |
| `06-create-from-pdf.yaml` | `bills_create_from_file` with raw bytes | One-shot path; agent should pick the convenience tool over the two-step. |
| `07-ocr-mindee.yaml` | OCR auto-fill via Mindee | When ocr_provider is configured, the agent shouldn't manually enter fields OCR can extract. Pins the "agent doesn't double-do extraction's job" behavior. |

Scenarios 5 + 6 require the **storage** app installed in the
project. The runner spawns it via `also_install`.

## Authoring guidelines

Same conventions as billing's scenarios — assert outcomes via REST,
end with "Then stop.", set budgets ~50% above best observed runs.
The cautious-mode + agent_response_contains assertion in
`04-reject-vs-void.yaml` is the pattern for any scenario where
a human-in-the-loop quote-back is required.
