# Processes 0.9.0

Processes now guides each agent's main thread to delegate ready steps to focused
workers. Workers read the durable step, execute within its scope, record their
outcome, and report once. Main remains the coordinator. Processes releases
subsequent steps and delivers them to each assigned agent.

Step and native task reads now include structured ancestor evidence: IDs, kinds,
states, outputs, explicit approval decisions, and direct-dependency flags.
Workers can verify approval from their step context without asking main to
retrieve another record. Existing dependency_outputs remains compatible.

The skill and delivery instructions reduce repeated reads, thread inventories,
manual cross-agent forwarding, and duplicate completion writes. Tasks remains
an optional integration. No new database migration or permission is required.
Workers need Processes MCP access when main delegates work.

## Validation

- Processes Go suite and 14 verifier tests passed.
- Five-step, three-agent live scenario passed on Codex / GPT-6 Astra: 203 seconds,
  40 iterations. Codex / GPT-5.6 Terra: 136 seconds, 41 iterations.
- Saved-state and trajectory checks verified five distinct workers spawned by
  main, authoritative worker reads, dependency ordering, approval and completion.
- Publication was a simulated local receipt; no external service was called.
- These are individual smoke runs, not a performance or reliability benchmark.

Minimum Apteva remains 0.51.3; app-sdk stays pinned to v0.81.0. The test CLI's
model override fix is test infrastructure, not a new runtime dependency.
