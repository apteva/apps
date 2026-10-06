# Processes 0.16.18

Run details now show live worker activity in the current-step panel and historical step cards, with chronological tool and model events, filters, expandable inputs and results, and readable app icons. Shared feeds use host telemetry and incremental recovery polling. Completed-step activity stops at its completion time. Model thoughts are displayed when the runtime records text; historical events without text say so explicitly.

The run graph and current-step card align, with distinct animated ready and waiting states and reduced-motion support. The step editor exposes shared general instructions, approval policy and completion criteria under “Process-wide rules also apply”, making approval requirements outside the step easier to find.

Worker prompts now instruct sequential and parallel workers to save a waiting or blocked checkpoint, its reason and recoverable output before requesting approval, reporting a blocker or calling pace. This is prompt guidance, not runtime enforcement. Existing procedure rules and frozen run definitions are preserved.

Verification:
- All 35 Playwright browser tests passed, including live activity, graph states and shared policy visibility.
- All 40 UI tests passed.
- Go short suite passed against pinned app-sdk v0.95.0 with GOWORK=off.
- TypeScript, production panel build/import validation, screenshot inspection and diff checks passed.
- Tier 3 was not rerun for this release.
