# Processes 0.19.3

Procedure versions are visible across the panel:

- The process list has a Version column.
- Project run cards show the procedure version each run used.
- The project Assignments tab shows the version an assignment will run, whether
  it follows the latest version or is pinned, and names the latest version when
  a pin is older.
- Assignment cards inside a process show the current version for assignments
  that follow the latest, and flag older pins the same way.

`GET /processes/assignments` now includes each process's `process_version`. No
migration, permission, MCP tool or Core/Server changes. Validated with the Go
suite, 46 Bun UI tests and a browser check against local data.
