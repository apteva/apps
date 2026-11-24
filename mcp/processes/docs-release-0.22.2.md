# Processes 0.22.2

Widget rows bring back the status badge.

0.22.1 replaced the badge with a colored dot. The badge (RUNNING, COMPLETED,
BLOCKED, FAILED…) is back at the top right of each row, with time and step
count under it. The context line never repeats the state: finished runs show
their summary, running runs the current step plus the live activity line and
progress, failed runs the reason.

UI only: no API, migration, Core, Server or SDK changes. Validated with the
Bun unit tests and a visual check in the widget test page.
