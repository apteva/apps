# Processes 0.16.2

Processes can retain one worker thread per run and executor agent across branches,
joins, timed steps and human approval. Worker responses also carry less repeated
context while preserving exact saved evidence.

- Set assignment `worker_continuity=per_executor`, or choose **Reuse one worker per
  agent** in the assignment editor. Ready steps on the same executor are serialized;
  different agents can work concurrently. `auto` keeps existing scheduling and
  `isolated` keeps separate workers. The policy is frozen for each run.
- Each step still has its own dependency checks, tracked execution identity,
  immutable delivery envelope, checkpoint and durable output receipt. Restarts and
  lost replies reuse saved worker ownership and dispatch identities. Validation
  and operator approval gates remain enforced.
- Worker `step_get`/`step_claim` return the assigned step and one authoritative
  `dependencies` manifest containing complete ancestor IDs, states and output
  receipts. They omit unrelated procedure steps and delivery diagnostics.
- Once shared policy is retained, `include_context=false` avoids returning policy,
  parameters, inputs and assignment details again. Omit the flag or set it to true
  to recover context. Reads always retain the step checkpoint and complete receipts.
- Worker `step_update` returns IDs, accepted state/revision/progress, run state,
  `done`, `next_action` and a blocker reason when present. It no longer echoes
  instructions or receipts. Continue after progress; await the next event after
  completion; call native done immediately when `done=true`. Explicit `run_get`
  and operator HTTP reads retain full inspection snapshots.
- Migration 014 preserves existing worker rows and keys ownership by run and agent.
  Assignment edits apply to future runs; previously dispatched events are unchanged.
  The SDK dependency is pinned to published v0.95.0, verified by commit ancestry.

## Validation

Go race, UI/verifier, browser and TypeScript checks cover continuity across
branches, joins, delays, multiple agents and runs; checkpoint recovery; ambiguous
delivery retries; immutable output identities; completion replay; approval gates;
and compact response sizes. A 13.5 KB output produces a 305–316 byte acknowledgement.

The Tier 3 fixture runs real local MCP operations with GPT-6.1 Sol and independently
checks both app databases and the tool trace. It requires one worker and prepared
context, retained tools/policy, two distinct artifact receipts, a validation join,
actual operator HTTP approval, one publication and one final done. Artifact names
simulate Media outputs; this is not a Media rendering performance benchmark.

The verified development run produced 13,547 bytes of worker claim/update payloads
versus 37,207 in the earlier continuity run, about 64% less. Model-authored procedure
text differs between runs; this does not establish a latency improvement.

The clean 0.16.2 release checkout passed the Go race suite, 73 UI/verifier tests,
19 Playwright tests, fixture tests, TypeScript checks and panel import validation.
Its GPT-6.1 Sol Tier 3 run passed all assertions and independent state verification
in 26 iterations and 208.216 seconds, reporting 503,064 tokens. It completed all
six steps with one worker and one prepared context, reused policy/tools, preserved
exact receipts and distinct execution IDs, and performed actual HTTP operator
approval before publication.
