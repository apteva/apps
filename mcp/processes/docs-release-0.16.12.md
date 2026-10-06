# Processes 0.16.12

The project-wide Runs view now opens a selected run as a full-width detail page
instead of keeping the run list beside a narrow detail card. The detail page
keeps the live status, current step, worker activity, tool calls, outcomes and
delivery blockers together, with a clear Back to runs action. Selection is
retained when a state filter changes so an operator can continue following the
same run.

The per-process Runs view keeps the same full-width detail behavior. Stored run
records, process definitions, dependency evidence, receipts and HTTP responses
are unchanged.

Verification: 124 Bun UI and scenario tests, TypeScript checks, panel
build/import verification, Go short tests and all 24 Playwright browser tests
passed.

The manifest version and runtime source ref both point to 0.16.12.
