# Processes 0.16.13

Run details use the entire available Processes panel width. The project-wide
and per-process Runs views share the same layout without the previous 1180px
cap. Selecting a run replaces the run list with its detail page; Back to runs
returns to the list. Live current-step and worker activity remain alongside the
run evidence on desktop and stack vertically on mobile.

Browser regression coverage verifies the actual detail width at 1800px and
375px, absence of the run list, preserved selection across state filter
changes, and return navigation.

Verification: 124 Bun UI/scenario tests, TypeScript checks, generated panel
build/import checks, Go short tests and 26 Playwright tests passed.

The manifest version and runtime source ref both point to 0.16.13.
