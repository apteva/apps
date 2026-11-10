# Processes 0.18.1

Processes wakes its dispatcher after committed changes and at the next saved
deadline. On startup, it recovers pending work from SQLite. An idle installation
runs a one-minute repair sweep.

- Trigger reconciliation reads status flags and loads configuration for pending
  or changed subscriptions.
- Worker cleanup, terminal notifications, and rating requests start from their
  pending indexes and retain their retry order.
- Scheduled assignments and workflow reconciliation select eligible work before
  loading procedure definitions. Existing runs continue to use their frozen
  procedure versions.

Migration 024 adds indexes for due assignments and open runs. This release pins
app-sdk v0.115.1.
