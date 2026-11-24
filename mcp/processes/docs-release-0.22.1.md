# Processes 0.22.1

Cleaner dashboard widget rows.

- Each row shows its state once: a colored dot (green finished, blue running
  and pulsing, amber blocked or waiting, red failed, purple scheduled) instead
  of a "Finished" label next to a COMPLETED badge.
- The context line under the name carries the rest: the run summary for
  finished runs, "Running · <step>" plus the live activity line and progress
  for running ones, the reason in amber or red for blocked and failed runs,
  and "Next in … · <assignment>" for schedules.
- Time and step count sit right-aligned in a narrow column, so the summary
  uses most of the width; rows size to their content.

Validated with the widget and scenario Bun tests and a visual check against
the local overview snapshot. UI only: no API, migration, Core, Server or SDK
changes.
