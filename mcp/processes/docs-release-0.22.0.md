# Processes 0.22.0

A redesigned Processes dashboard widget.

- Clicking a run opens its details in a modal above the dashboard instead of
  expanding the widget. Backdrop click or Escape closes it and focus returns
  to the row; on phones it is a bottom sheet.
- The modal shows the run summary the last agent wrote, progress, a selectable
  step list and that step's live activity: the agent's thoughts and the tools
  it ran (latest 8, "Show all" for the rest).
- Running rows show one live line (the latest thought or tool call) and a
  progress bar. Finished rows show the start of the run summary.
- Isolated step workers' thoughts now appear: the widget reads model events
  from the step's dedicated worker thread.

API: `GET /overview` items and steps gain `thread_id` and `execution_id`;
items gain `summary` (the latest run summary outcome, else its blocker,
clipped to 600 characters). Additive only.

Validated with `GOWORK=off go test ./...` (one new test), the widget Bun tests
(one new test) and a visual check on desktop and phone widths against a
replayed real run. No migration, Core, Server or SDK changes.
