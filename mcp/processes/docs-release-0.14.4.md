# Processes 0.14.4

This patch release makes the first delivery to an app-provisioned worker use
the lifecycle-tracked agent event API. Creating a worker thread no longer
counts as delivering its first step, so every independent and sequential step
persists the Core `execution_id` returned for its authoritative delivery.

Worker-facing Processes calls can recover the canonical process from durable
run and step IDs, and stale sequential work receives a throttled reminder on
the same worker thread. Workers continue to inherit the executor agent's
spawnable MCP-server scopes through the platform thread API.

The bundled Processes panels are also forced through the production JSX build
path, avoiding `react/jsx-dev-runtime`/`jsxDEV` imports in the host dashboard.

Terra Tier 3 validation passed for event-triggered execution and browser-session
continuity. This release changes Processes and shared panel build output only;
Conversations is unchanged.
