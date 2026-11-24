# Processes 0.23.1

A one-line live row, with app icons.

- Running widget rows have a single activity line: the latest tool call with
  its app or integration icon (the step name before the first call), and the
  live marker on the right ("Thinking 6s", "Running", "Starting"). The marker
  is never clipped; the action text shortens instead.
- The thought line and the three-action trail from 0.23.0 are gone from the
  card. Hovering the line shows the step, the latest call and the last
  thought; the details modal keeps the full timed list.
- The details modal shows each action's app or integration icon.

Icons come from the project's installed apps and connections, the same
sources the Processes panel uses, fetched once per project. UI only: no API,
migration, Core, Server or SDK changes.
