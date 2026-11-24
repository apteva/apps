# Processes 0.23.0

Live activity you can follow in the dashboard widget.

Measured on a real two-step run, tool calls took 1–2% of each step (0.6 s of
30 s); the rest was model turns of 2–10 s. Showing only the newest event
meant tool calls flashed by and a stale thought stayed up during long turns.

Running rows (fixed height, two short lines above the progress bar):
- the last finished thought, in quotes; it changes only when a turn ends;
- the last three completed actions in plain words (the agent's stated reason,
  or "Saved evidence", "Completed step" for Processes' own calls), newest
  first so a narrow card never hides the latest;
- a live marker: "Thinking 6s" while the model works, the action while a tool
  runs, or "Starting 3s" before the first event.

Details modal, per step: total time with a thinking-vs-tools split, every
thought and action in order with its duration, and arguments and result on
click; an in-progress turn counts up.

Calls a dedicated step worker makes before claiming its event (the first tool
lookup) are now included. UI only: no API, migration, Core, Server or SDK
changes. Validated with the Bun unit tests (one new), the Go suite and a
visual check against a replay of a real local run.
