# Processes 0.16.22

Worker handoffs are compact and detailed diagnostics are stored separately.
`step_update.handoff` records outcome/decision, exact artifact IDs, essential
validation results, limitations, evidence references and blockers. Agent output
plus error is limited to 4096 UTF-8 bytes, including small legacy output strings.
Oversized writes fail without changing step state and explain how to save full
diagnostics with `step_evidence_save`. Existing completed receipts remain readable
and unchanged; identical completion retries retain their previous behavior.

`step_evidence_save` stores immutable, named diagnostics with exact artifact/source
references and a content hash, up to 16 KiB JSON. Stable keys make identical retries
idempotent. Only the assigned executor may save them, and evidence never completes
or approves a step. Migration 019 adds their append-only storage.

Worker claims/reads now return direct dependency handoffs with an 8 KiB inline
receipt budget and a 16 KiB overall worker-response budget. Indirect ancestors,
oversized legacy receipts and large context remain available through exact named
references. `step_read` retrieves current instructions, shared policy, one resolved
parameter, a checkpoint, one ancestor result, or one artifact/evidence record.
Normal worker recovery no longer reconstructs full run JSON through byte offsets.
Explicit full run/export inspection keeps its existing lossless paging behavior.

Dispatch events contain IDs and recovery instructions instead of repeating the
procedure and ancestor outputs. Identical long shared context values appear once
per worker response with value references. Frozen audit objects retain original
values. Shared policy is preserved without guessing which rules are applicable;
parameters and diagnostics remain data rather than additional authorization.

The embedded Processes guidance explains compact outputs, separate evidence,
context resolution and targeted rereads before consequential actions. Operator
HTTP inspection supports the same named reads. Complete instructions remain
cheap to retrieve; the app does not assume Core retains old model context.

Verification:
- Five GPT-6.1 Sol tier 3 scenarios passed with independent state/tool-trace
  verification: compact handoffs (44 iterations), legacy executor continuity (27),
  actual AI-directed parallel work (37), step-by-step controls (45), and memory
  across two runs (42). Exact artifact/source/digest receipts, worker ownership,
  real HTTP approval and final validation/publication gates were preserved.
- Final compact handoffs were 89–189 bytes; claims were 2267–4292 bytes. A 30-step
  regression reduced 601,237 bytes of ancestor/shared context to a 3558-byte worker
  response while retaining exact named recovery. These are payload/correctness
  measurements rather than an end-to-end latency benchmark.
- Go short/integration suites and race checks passed. All 167 UI/scenario tests,
  44 Playwright browser tests, TypeScript and production panel/import checks passed.
- Post-run SQLite evidence inspection now permits WAL journal bookkeeping while
  enforcing query-only SQL. Regression tests cover missing sidecars, pending WAL,
  write rejection and missing files. Fresh live reruns passed after this test
  harness correction.
- The Processes SDK pin is v0.102.0, reverified as the latest published ancestor
  of origin/main. Core, Server and SDK sources were not modified.

Only Processes application sources and marketplace release metadata change.
