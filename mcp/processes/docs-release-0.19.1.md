# Processes 0.19.1

Run cards in the project and per-process run lists now draw their state colour
as an inset stripe rather than a 3px left border. Previously, states without a
colour (waiting, blocked, queued, scheduled) kept a transparent left border, so
the hover and selection outline was missing on the left and its corners looked
cut. The outline is now even on every side, and waiting, scheduled, blocked and
queued runs get stripes matching their status pills.

UI-only change: no API, migration, permission or Core/Server changes. Validated
with 46 Bun UI tests, the Go suite and a browser check against local data.
