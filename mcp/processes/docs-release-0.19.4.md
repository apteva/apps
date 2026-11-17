# Processes 0.19.4

A step's tool activity is limited to events between its claim and its
completion. The tool call that completes a step (usually `step_update`) returns
a few milliseconds after the step's `completed_at`, so its result was cut off
and the call kept showing as running on finished runs.

Results that arrive after completion are now kept when they answer a call made
during the step. Calls started after completion stay excluded.

UI-only change: no API, migration, permission or Core/Server changes. Validated
with 47 Bun UI tests (one new), the Go suite and a browser check against a real
completed local run.
