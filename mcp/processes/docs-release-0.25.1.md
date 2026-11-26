# Processes 0.25.1

Fixes the assignment badge in the dashboard widget, which showed "Running"
for any assignment with an open run, even when every open run was blocked.

- `/processes/overview` assignments carry `live_run_state`, the state of the
  open run the row links to. When an assignment has several open runs it
  links to the most active one (running, then starting, waiting, blocked).
- The badge follows that state: Running, Starting, Waiting or Blocked (amber).
- An assignment whose open run is blocked counts as needing attention, with
  the warning "Run blocked", unless a trigger problem is already reported.
