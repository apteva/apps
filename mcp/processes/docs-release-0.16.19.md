# Processes 0.16.19

Procedure editors now put Save and Cancel in a separate compact bar above the editor. Add step and Fit flow retain normal button sizing. The full procedure form also puts saving above its fields.

The parameter editor uses compact aligned rows, a Required checkbox and a small remove button, with responsive layouts for narrow editor columns and mobile. Text, number and boolean defaults retain their values, including zero and false. The obsolete per-page wording is removed.

Run outputs render as labelled structured sections or sanitized Markdown. The final process result opens through View result in a formatted, keyboard-accessible modal instead of being repeated above the graph and in the side panel. Numeric receipt identifiers retain their exact text. Run and step progress use distinct labels and thicker bars capped at 300 pixels in the side panel.

Clicking a graph step displays its frozen instructions, required output, saved receipts, approval controls and worker activity in one side panel. Run details use this layout from both project Runs and individual process Runs. Live runs follow current work until a historical step is selected; explicit selection stays pinned through updates, and Follow current step restores live tracking. Historical activity remains scoped to the selected step and completion time. The separate detail cards below the graph are removed.

Uses app-sdk v0.96.0, the latest release by commit topology. Only Processes application code and release metadata changed.

Verification:
- All 41 Playwright browser tests passed; final focused selection/activity checks also passed.
- All 40 UI tests passed.
- Go short suite passed with GOWORK=off against the pinned SDK.
- TypeScript, production panel build/import validation, desktop/mobile screenshots and diff checks passed.
- Tier 3 was not rerun for these UI changes.
