# Processes 0.16.5

Structured runs can now pause between steps for operator review. Choose **Run
step by step** on an active assignment, or set `control_mode: "step_by_step"`
when starting through MCP. Automatic runs retain their existing behavior.

- Run detail offers **Run this step**, **Run next step**, and **Run selected
  steps** for independent branches, with saved outputs and approval evidence.
- `processes_run_advance` and the corresponding HTTP advance route release an
  exact eligible step with a stable idempotency key. Ready work remains held
  until explicit release, including after restart or checkpoint recovery.
- The control mode is frozen into each run. Workers cannot release downstream
  steps; dependencies, timing, ownership, capacity, validation and human approval
  still apply. Advancing a human step does not approve or complete it.
- Release authorization is saved before network delivery. Retries preserve
  exact delivery identities and cannot dispatch the same logical release twice.
  Release events refresh other open run views even while an executor is busy.
- Compact advancement receipts include exact IDs, saved state, eligible/active
  step identities, delivery warnings and a full-state reread reference.

Verification completed before release:

- Full Go race suite: passed (112.293 seconds).
- 123 Bun UI/verifier tests, 21 Playwright tests and TypeScript checks: passed.
- GPT-6.1 Sol tier 3: all eight steps completed with one prepared context and
  one durable executor worker. Independent child operations overlapped for
  18.824 seconds. Seven later steps were independently observed held before
  HTTP release; initial release and retry used MCP. Eight durable releases,
  exact output receipts, validation dependencies and real HTTP human approval
  before publication were verified independently from storage and tool traces.
  The scenario passed in 45 iterations and 356.325 seconds.
- Live advance responses were 837 and 836 bytes. Regression tests verify large
  frozen instructions remain exact on reread without being echoed by advance.
- Race regressions cover concurrent duplicate releases, ambiguous remote
  acceptance, restart, branch eligibility, schedule gates, cancellation,
  project scope, worker authorization, HTTP route IDs and approval preservation.

Changes are confined to Processes application code, UI, documentation and tests.
