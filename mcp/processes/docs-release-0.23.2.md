# Processes 0.23.2

See which step a running process is on.

- The widget row's title line names the current step after the process:
  "CRM Stats via Pushover › Send via Pushover". Parallel steps show
  "Read CRM +1". On a narrow card the step name shortens first.
- The progress bar has one segment per step: green when done, blue and
  pulsing while running, amber when blocked or waiting, red when failed, grey
  when not reached. Hovering a segment names the step and its state. The
  details modal uses the same bar.

No extra lines: the row keeps its height. UI only: no API, migration, Core,
Server or SDK changes.
