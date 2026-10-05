# Processes 0.16.3

MCP discovery and direct-run tools return less repeated procedure content and
provide exact reread references for recovery. HTTP/UI objects and stored immutable
procedure definitions retain their existing behavior.

- `list` returns discovery metadata and exact numeric owner IDs, with references
  to full immutable procedure reads.
- `get` returns current content once and historical version metadata. Requesting
  a version returns that exact definition and procedure metadata. Historical
  definitions are loaded only on request.
- `run_get` preserves the frozen definition and exact run/step state, outputs and
  operator approval evidence. It omits the duplicate textual snapshot and step
  definitions already present in the procedure.
- `run_update` returns saved IDs, procedure version, state/progress, completion,
  blockers and a reread reference instead of echoing instructions or results.
  Existing compact step reads/updates also gain exact reread references.
- References name existing tools and exact arguments. They recover stored evidence
  and full frozen step context; no model summaries or truncation are introduced.

Regression tests exercise the actual MCP callbacks, historical and run definition
fidelity, complete approval receipts, exact numeric serialization, valid recovery
references, completion replay, authorization and unchanged HTTP objects. In the
large-context fixture, MCP `get` is about 14.2 KB versus 64.5 KB for full HTTP
inspection; a direct update acknowledgement is 325 bytes versus a 54 KB full
snapshot. These measure payload size, not model latency.

Release verification on 2026-10-05 passed:

- Full Go race suite, 86 Bun UI/verifier tests, 19 Playwright tests, fixture
  tests, TypeScript checks and manifest validation.
- Live MCP recovery scenario using `openai-codex` / `gpt-6.1-sol`: 14 iterations,
  92.436 seconds. Both immutable versions were recovered by reference, only
  version 2 was executed, and the exact saved result was reread. Live discovery
  was 790 bytes and the completion receipt was 325 bytes.
- Live worker continuity regression using the same model: 26 iterations,
  216.428 seconds. All six steps completed with one worker/context/discovery,
  separate exact portrait receipts, validation, actual operator HTTP approval
  and publication. Both live runs passed independent saved-state/tool-trace
  verification.
