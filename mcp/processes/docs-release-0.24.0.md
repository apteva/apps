# Processes 0.24.0

The project Assignments view says how each assignment starts.

- `GET /processes/assignments` returns each assignment's event triggers
  (`triggers`: id, name, topic, source_install_id, status,
  subscription_enabled, sync_error), loaded with one query per project.
- The Schedule column is now "Starts": the schedule with its next run, each
  live trigger ("On event" with its name and topic, flagged when paused, not
  listening or failing to sync), or "Manual only".
- The "On a schedule" stat is now "Start automatically": active assignments
  with a schedule or a listening trigger.
