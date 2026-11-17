# Processes 0.19.0

The panel gains a project-wide **Assignments** tab. It lists every assignment in
the project with its process, coordinating and role agents, schedule and next
run, total and in-progress run counts and the latest run's state. Clicking an
assignment opens Runs filtered to it; the latest run opens directly. The list
refreshes from process, assignment and run events on the app bus.

Project Runs can be filtered by process and by assignment, and paging ("Load
older runs") now sits at the end of the list instead of above it, which also
stops it overlapping the first run card's border. Run cards keep their date and
current step on separate lines.

Assignments that bind roles to several agents now show all of them: the
process list's Agents column and owner filter include role agents, and each
assignment card lists who handles each step (unbound roles fall back to the
owner, marked as the default).

New read routes: `GET /processes/assignments` (project-scoped, run statistics
from the existing assignment index) and an optional `process_id` on
`GET /processes/runs` that is refused for processes outside the project. No
migrations, permissions, MCP tool or Core/Server changes.

Validated with the complete Go suite, a new route test, 46 Bun UI tests and a
browser check against local data.
