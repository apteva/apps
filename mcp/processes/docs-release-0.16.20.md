# Processes 0.16.20

Run details prioritize worker activity. The selected step shows a compact two-line instruction preview and a View step details action. Full frozen instructions, dependency information, required output, timing, step progress and saved receipts open in a formatted modal. This applies to both live and historical runs from project and procedure entry points. Opening details pins that step through live updates; Follow current step restores live tracking. Human completion and step-by-step release controls remain available.

Selected graph steps use one accent border, including keyboard selection. State colors remain visible in the status badges. The separate state border, inset stripe and selection outline no longer stack on selected nodes.

Discovery and setup now explain executor requirements. Processes automatically attaches its coordination tools before dispatch; executors need any apps required by their steps attached and configured. The manifest, runtime assignment_create/assignment_update/start descriptions, Processes skill and marketplace summary carry this guidance.

Verification:
- GPT-6.1 Sol live MCP recovery passed in 14 iterations and 89.506 seconds. The independent verifier confirmed immutable version fidelity, compact completion receipts, exact reread references and saved-result recovery.
- GPT-6.1 Sol live executor continuity passed in 27 iterations and 164.623 seconds. The independent verifier confirmed one persistent worker, one preparation, retained fixture tools/context, exact artifact receipts, a validation join, HTTP human approval and publication after approval.
- All 128 UI and scenario-verifier tests passed.
- All 42 Playwright browser tests passed on the final full run. An initial keyboard-selection timeout also passed three consecutive focused reruns.
- Go short suite passed with GOWORK=off against pinned app-sdk v0.96.0, verified latest by commit topology.
- TypeScript, production panel build/import checks, metadata consistency and diff checks passed.

Only Processes application files and marketplace release metadata changed. Procedure versions, frozen execution definitions and saved receipts retain their existing storage format.
