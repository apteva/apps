# Processes 0.14.6

This release adds a project-wide Runs browser beside Processes and Project map.
It lists recent executions across every process, groups ongoing and
attention-needed work, and opens one selected run in a detailed, readable view.
The per-process Runs tab now uses the same list-and-detail interaction instead of
expanding every execution at once.

Agent step details now show the tool calls made for that exact execution. Tool
activity is loaded lazily from authenticated platform telemetry and correlated by
the durable step execution ID, which keeps sequential steps isolated even when
they share one persistent worker thread. Each call includes its `_reason`, outcome,
raw tool name, and the icon and name of the providing app or integration when
available. Direct single-agent runs expose the same activity view.

The new `GET /processes/runs` route is read-only, project-scoped, bounded to the
latest 200 runs, and includes process identity. Existing process-specific history,
permissions, lifecycle controls, and deep links are unchanged.

Regression coverage includes project isolation, route output, cross-process run
browsing, selected run details, exact execution correlation, provider icon
resolution, live refresh, timing, and browser interaction.
