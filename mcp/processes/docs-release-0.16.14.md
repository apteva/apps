# Processes 0.16.14

Run history now uses restrained status colors that follow the Processes theme.
Running and ready work use blue, waiting and scheduled work use purple,
completed work uses green, and cancelled or failed work uses rose. Run cards
also carry a subtle matching edge accent, so outcomes remain scannable without
turning the list into a high-contrast dashboard. The same treatment is used in
project-wide and per-process run lists.

Verification: Processes UI tests, TypeScript checks, generated panel
build/import checks and all 26 Playwright browser tests passed.

The manifest version and runtime source ref both point to 0.16.14.
