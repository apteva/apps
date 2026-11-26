# Processes 0.25.2

The dashboard widget on All projects Home now follows the global widget
contract.

- `/processes/overview?scope=global` (global installs only) requires a
  signed-in user, refuses delegated application users, and lists projects
  with the viewer's session, so the widget only shows projects the viewer can
  open. A selected `project_id` outside that list returns 403. Any other
  `scope` value returns 400. Requests without a scope from older widgets get
  the same checks.
- Rows in the global response carry `agent_name`, read once per project. The
  widget uses them instead of listing agents in the browser.
- In All projects the widget reloads every 15 seconds while visible, since it
  gets no live app events there.
- Inside a project nothing changes.
