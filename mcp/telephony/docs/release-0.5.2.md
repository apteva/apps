# Telephony 0.5.2

Let platform principals manage inbound routes. Until now only the agent that
answers a number could create, configure, list, or disable its route: the MCP
tools took the owner from the caller context and refused any caller without an
agent id. An API key or a dashboard session could publish flows and
destinations but could never bind a carrier number, so the last step of a
fully scripted setup had to be done by an agent.

## What changed

- `telephony_routes_create` accepts an optional `agent_id`. Agents still own
  the routes they create and cannot name another agent. A caller that is not an
  agent (API key, panel session) must pass `agent_id`; the agent is verified
  against the current project through the platform agent directory.
- `telephony_routes_list` returns every project route for a non-agent caller.
  Agents keep seeing only their own routes.
- `telephony_routes_set_answer_mode`, `telephony_routes_configure_carrier`,
  `telephony_routes_set_transport`, `telephony_routes_set_recording_policy`,
  and `telephony_routes_disable` share one ownership rule: an agent may only
  touch routes it owns, while a non-agent caller is scoped to the project.
- New panel endpoints, all behind the existing project and application-user
  checks: `POST /numbers/agents` lists the agents of the project,
  `POST /numbers/routes/create` creates a route for an explicit `agent_id` and
  configures the carrier in the same call (`configure: false` skips that;
  `flow_id` also assigns a published flow), and `POST /numbers/routes/disable`
  restores the previous carrier webhook and disables the route.
- The Connected numbers panel shows a "Create route" control on unrouted
  numbers, with an agent picker and answer mode, and a "Disable" button on
  routed numbers.

Delegated application users remain excluded from every route tool and route
endpoint. Route creation, carrier configuration, and the call-time behaviour of
existing routes are unchanged for agents.

## Scripted setup with an API key

```text
telephony_routes_create            {phone_number: "+33189313431", agent_id: 5, answer_mode: "realtime_immediate", directive: "..."}
telephony_routes_configure_carrier {route_id}
telephony_flows_assign_numbers     {flow_id, route_ids: [route_id]}
```

The panel equivalent is one call to `POST /numbers/routes/create` with
`agent_id`, `configure: true`, and an optional `flow_id`.
