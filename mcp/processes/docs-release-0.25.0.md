# Processes 0.25.0

The dashboard widget now shows assignments that start on events.

- The "Scheduled" tab is replaced by an **Assignments** tab listing every
  active assignment on an active SOP: scheduled ones by next run, then
  event-driven ones, then manual ones. Each row shows how it starts (readable
  schedule, or source app and event title), project, agent, last run, and a
  badge: Running, Next in…, On event, Manual, or a trigger warning such as
  "not listening". Rows link to the assignment, or to its live run.
- **All** keeps live runs, next scheduled runs and history, plus one line
  "N assignments waiting for events · M need attention" that opens the
  Assignments tab.
- `/processes/overview` returns `assignments` (capped at 40) and the counts
  `assignments`, `event_driven` and `assignment_issues`, for one project and
  for the global scope.
