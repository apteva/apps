# Processes 0.16.11

This release fixes worker provisioning for structured process runs. Independent
and persistent Processes workers now request the `processes` MCP scope
explicitly, so they can discover and call `processes_step_claim`,
`processes_step_get`, and `processes_step_update` even when inherited tool
catalog state is stale. The main agent remains an observer/coordinator and
cannot complete an independently dispatched step.

Run inspection now opens a selected run in a full-width detail view with live
refreshing, an animated status indicator, the current step and worker activity
on the right, recorded tool calls, and delivery or worker blockers. Active runs
refresh every two seconds while the existing event stream remains available.

Verification includes the full Go short suite, Processes unit and scenario
tests, 24 browser tests, TypeScript checks, generated panel build/import checks,
and regression coverage for the explicit Processes MCP worker scope.

The manifest version and runtime source ref both point to 0.16.11.
