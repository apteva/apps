# Processes 0.14.2

This release makes worker provisioning app-owned and capability-safe. Processes
creates independent and persistent sequential workers through the platform
thread API, sends the authoritative step event atomically, and lets the
platform inherit the executor agent's spawnable MCP-server scopes. Agents no
longer need to enumerate domain servers, spawn workers, or call
`processes_step_assign`.

Sequential workers remain bound to one durable thread across dependent steps;
independent steps get isolated workers with the required Processes coordination
surface. Delivery retries preserve worker and event identity.

The Tier 3 verifiers now cover app-owned worker traces, event-triggered runs,
and browser-session continuity. Terra validation passed for multi-agent,
event-trigger, sequential-worker, operator-confirmation, and browser-continuity
scenarios. This release changes Processes only; Conversations is unchanged.
