# Processes 0.16.6

This patch release hardens persistent worker handoffs. Transport settlement is
no longer treated as proof that a worker claimed a step.

- Delivered ready steps now have durable claim evidence. If a persistent worker
  leaves a delivered step unclaimed, Processes sends bounded recovery wakes on
  the same worker and preserves the exact process, run, and step identities.
- After three unsuccessful recovery attempts, the step and run report an
  actionable stalled state. A late valid `processes_step_claim` clears the
  recovery state and continues from the saved checkpoint.
- Lifecycle settlement cannot erase an unclaimed-step recovery warning.
- Worker acknowledgements and handoff wording prioritize delivered ready work
  over an earlier completion response that said to wait. Auto-parallel workers
  keep intentional ready-branch choices while another ready branch is present.

Verification completed before release:

- Full Processes Go race suite passed.
- 123 Bun UI/verifier tests and TypeScript checks passed.
- Processes panel build and host React-surface import verification passed.
- GPT-6.1 Sol tier 3 completed the eight-step controlled workflow with one
  persistent worker, independent child overlap, exact receipts, and real HTTP
  human approval before publication.
- New recovery regressions cover settlement without claim, restart-safe bounded
  wakes, late claim recovery, and intentional parallel branch choice.

Changes are confined to Processes application code and its migration/tests.
