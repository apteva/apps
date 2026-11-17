# Processes 0.19.2

Run cards in the project and per-process run lists no longer draw a coloured
stripe or tint on their left edge. Cards are plain, the status pill alone shows
each run's state, and hover and selection keep an even outline on every side.

UI-only change: no API, migration, permission or Core/Server changes. Validated
with 46 Bun UI tests, the Go suite and a browser check against local data.
