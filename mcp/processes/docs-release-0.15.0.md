# Processes 0.15.0

This release makes process authoring semantic and graph layout deterministic.
Agents define steps and dependencies; Processes derives the visual layout every
time. The MCP step schema no longer accepts `position`, newly saved immutable
versions strip legacy coordinates, and the editor no longer drags or persists
cards. Existing versions remain compatible, but their stored coordinates are
ignored, so previously overlapping workflows render correctly immediately.

A new read-only `validate_definition` MCP tool returns the normalized definition
and structured readiness: validation errors, warnings, step and required
parameter counts, approval coverage, and suggested next actions. Procedure reads
include readiness too. Approval prose without a real approval step is clearly
reported as advisory rather than an enforced gate.

The agent-facing descriptions now spell out the safe lifecycle: create an
unassigned draft, create a paused assignment, explicitly activate the reviewed
process and assignment, and explicitly authorize starting a run. No creation
action implicitly deploys or executes work.

Overview and Procedure now show a readiness card with step, parameter,
assignment, and approval-gate counts. Regression coverage proves overlapping
legacy coordinates are ignored, semantic save payloads contain no presentation
data, agent schemas do not expose coordinates, warnings identify unenforced
approvals, and creation/deployment guidance remains explicit.

Persistent sequential workers also receive an unambiguous top-level `done` and
`next_action` after every step update. The final response tells the worker to
call its native completion tool immediately; non-final responses tell it to wait
for the next app-owned delivery without polling.

Live Tier 3 validation passed with `openai-codex` / `gpt-6-sol`: the sequential
fixture completed three ordered steps with one persisted worker and one final
completion call, and the browser fixture reused one live browser session across
three steps, preserved page state, submitted exactly once, and closed exactly
once.
