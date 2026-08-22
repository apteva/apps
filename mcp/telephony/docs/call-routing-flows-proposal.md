# Telephony Call Routing Flows Proposal

Status: MVP implemented (versioned flows, validation/simulation, legacy-route
migration, schedules, caller rules, announcements, Twilio/Telnyx DTMF,
browser/agent/AI destinations, ring-group offers, execution traces, events, and
the Routing UI). External PSTN/SIP child-leg bridging, durable queues,
decision webhooks, and non-Twilio voicemail remain later delivery phases and
are rejected by route capability validation rather than failing during a call.

This document proposes a carrier-neutral call-routing engine inside the
Telephony app. It is intentionally not an implementation plan for Core or the
generic Workflows app.

## Decision

Build the live call-routing engine inside Telephony. Keep Workflows optional
for post-call business automation through Telephony events.

```text
Carrier number
  -> inbound route
  -> published call-flow version
  -> conditions, IVR, and destinations
  -> agent realtime bridge, PSTN, SIP, voicemail, or hangup
```

Core remains unaware of carriers, IVRs, queues, and ring groups. Telephony
selects an agent and only then creates or attaches the generic realtime thread.

## Routing Model

Retain one active inbound route per carrier number, but make the route target a
published flow rather than one agent:

```text
Current: number -> route -> agent
Target:  number -> route -> published flow -> destinations
```

Each call pins the exact flow version it started with. Publishing a new version
only affects new calls.

### Shared flows across inbound numbers

One published flow may serve many inbound routes, including routes backed by
different carriers. Each number still has exactly one active flow assignment.
Bulk assignment validates every selected route before opening a transaction;
if one route cannot support the flow, no assignment is changed.

Assignments may store project-safe per-number variables such as `brand`,
`locale`, `alias`, `timezone`, `greeting`, `tags`, and metadata. Flow and
destination strings may reference scalar values with templates such as
`{{number.brand}}`; the runtime expands them only after pinning the immutable
published version. `recording_mode` is validated against the number's provider
and transport and updates the route recording policy in the same transaction.

Publishing a shared flow affects future calls on every assigned number. The UI
therefore shows all assigned numbers and offers an explicit duplicate action
for creating an independent flow before editing.

## Flow Nodes

| Category | Nodes |
| --- | --- |
| Control | Schedule, holiday, caller match, metadata condition, branch |
| Interaction | Announcement, DTMF menu, language selection |
| Routing | Agent, ring group, queue, external number, SIP |
| Side effect | Emit event, notification webhook, decision webhook |
| Policy | Recording, maximum duration, timeout |
| Terminal | Voicemail, reject, hangup |

DTMF should be the first IVR input mechanism. Speech-based menu routing can be
added later. AI conversations remain agent destinations rather than becoming
special cases in the routing language.

## Ring Groups

Ring groups should support:

- `simultaneous`: notify every eligible agent; the first atomic claim wins.
- `round_robin`: rotate through available members.
- `longest_idle`: choose the least recently assigned member.
- `priority`: try ordered tiers.
- `weighted`: distribute using configured weights.
- `sequential`: try each member with a per-member timeout.

Each membership stores priority, weight, offer timeout, maximum concurrent
calls, and enabled state.

Agent presence uses `available`, `busy`, `wrap_up`, and `offline`, with
heartbeat expiry. Unknown or stale presence is unavailable unless the group
explicitly permits it.

## Destination Answering

An agent destination supports:

- `agent_offer`: emit `call.offered`; the agent creates a realtime thread and
  atomically claims the call with its thread ID.
- `realtime_immediate`: Telephony selects the agent and immediately spawns one
  realtime thread.
- `external`: dial a PSTN or SIP destination without spawning a thread.

Automatic routing must create only one realtime thread. Simultaneous offers
must not create one thread per ring-group member.

## Queues

Queues require:

- Maximum wait time and maximum caller count.
- Announcement interval and optional estimated-wait messaging.
- Overflow and no-answer destinations.
- Caller-disconnect cancellation.
- Durable state across Telephony restarts.

Late claims, duplicate provider callbacks, and repeated transitions must be
rejected or replayed idempotently.

## Webhooks

Two separate webhook concepts are required.

### Notification Webhook

An asynchronous observer used to store call data or update external systems.
It never delays or controls the call.

### Decision Webhook

A synchronous routing decision with a strict two-to-three-second deadline and
a mandatory local fallback. Responses may select only validated Telephony
actions. Raw TwiML, carrier XML, credentials, and arbitrary provider commands
must not be accepted.

Both webhook types require HMAC signatures, delivery IDs, idempotency keys,
encrypted secrets, URL redaction, SSRF protection, bounded retries, and
delivery diagnostics.

Ordinary HTTP webhooks do not carry audio. Audio destinations use SIP or a
provider media WebSocket.

## Provider Boundary

Extend the existing carrier adapters behind a generic call-control interface:

```go
type CallControl interface {
	ConfigureInbound(...)
	Answer(...)
	Play(...)
	GatherDTMF(...)
	DialNumber(...)
	DialSIP(...)
	StartMedia(...)
	StartRecording(...)
	Redirect(...)
	Hangup(...)
}
```

Each adapter declares its capabilities. Publishing a flow fails when the
selected provider cannot execute a required node.

Twilio should receive the first complete implementation. Telnyx and Plivo
should implement the same contract next, followed by SignalWire and Vonage.
A Ringover-forwarded call entering through Twilio follows the Twilio flow.

## Persistence

Add project-scoped tables:

- `routing_flows`
- `routing_flow_versions`
- `routing_destinations`
- `ring_groups`
- `ring_group_members`
- `agent_presence`
- `call_route_executions`
- `call_node_executions`
- `call_offers`
- `webhook_endpoints`
- `webhook_deliveries`

Add `flow_id` and `published_flow_version_id` to `inbound_routes`. Add the
selected flow version and destination to `calls`.

Reuse the existing `call_events` durability model and inbound event outbox
instead of introducing another event system.

## Flow Validation

Before publication, validate that:

- There is exactly one entry node.
- Every reachable path terminates or has a bounded wait.
- There are no dangling edges or unbounded cycles.
- Node count, graph depth, and total timeout budget are bounded.
- Every destination belongs to the same project.
- Every required action is supported by the route's provider.
- Decision webhooks and external destinations have local fallbacks.
- Secret-bearing data is referenced, not embedded in the flow definition.

Provide a side-effect-free simulator that accepts caller number, called number,
time, agent presence, and metadata, then returns the path and decisions.

## Events

Declare additional events in `apteva.yaml`:

- `call.routing.started`
- `call.routing.node_entered`
- `call.offered`
- `call.offer.claimed`
- `call.queued`
- `call.transferred`
- `call.voicemail.started`
- `call.routing.failed`
- `webhook.delivery.failed`

Lifecycle payloads should include flow ID, flow version, destination ID,
ring-group ID, selected agent, attempt number, and routing reason. They must not
contain credentials, webhook secrets, or complete secret-bearing URLs.

## MCP Surface

Project-management tools:

- `telephony_flows_create`
- `telephony_flows_update`
- `telephony_flows_get`
- `telephony_flows_list`
- `telephony_flows_validate`
- `telephony_flows_simulate`
- `telephony_flows_publish`
- `telephony_routes_set_flow`
- `telephony_flows_validate_numbers`
- `telephony_flows_assign_numbers`
- `telephony_flows_unassign_numbers`
- `telephony_flows_list_numbers`
- `telephony_destinations_create`
- `telephony_destinations_update`
- `telephony_destinations_list`
- `telephony_destinations_test`
- `telephony_ring_groups_create`
- `telephony_ring_groups_update`
- `telephony_ring_groups_set_members`
- `telephony_ring_groups_list`
- `telephony_webhooks_create`
- `telephony_webhooks_update`
- `telephony_webhooks_test`
- `telephony_webhooks_list_deliveries`

Agent runtime tools:

- `telephony_agent_presence_set`
- `telephony_calls_list_offers`
- `telephony_calls_claim`
- `telephony_calls_decline`
- `telephony_calls_transfer`

Existing route tools remain compatible. `telephony_routes_create` creates a
single-agent flow equivalent to the current behavior.

## UI

Add a dedicated Routing view containing:

- Number-to-flow overview with carrier webhook health.
- Ring-group and destination management.
- Full-width flow editor with node palette, canvas, and configuration panel.
- Draft, validation, simulation, publication, rollback, and version history.
- Test caller context showing the exact simulated path.
- Active-call routing trace.
- Webhook delivery diagnostics.
- Provider capability warnings.

The Numbers view continues to show the assigned flow, published version,
primary destinations, and routing health for every connected number.

## Migration

Create an idempotent migration that converts each existing inbound route into
a generated one-agent flow while preserving:

- Route ID and callback secret.
- Carrier connection, phone number, and provider number ID.
- Assigned agent and answer mode.
- Directive, voice, greeting, hold prompt, and timeout.
- Recording policy.
- Previous carrier webhook and status callback values.

Do not rewrite carrier webhooks during migration. Existing routes must behave
identically until their generated flow is edited and explicitly published.

## Workflows Integration

The Workflows app is not a runtime dependency. It may subscribe to Telephony
events for asynchronous business operations such as:

- Updating a CRM.
- Storing call metadata.
- Processing a completed recording or transcript.
- Creating follow-up tasks.
- Sending notifications.
- Starting appointment or reporting workflows.

No Workflows call belongs in the active media or provider callback path.

## Delivery Phases

1. Flow schema, validation, versioning, migration, simulation, and legacy
   compatibility.
2. Agent destinations, presence, ring groups, atomic claims, queues, and
   overflow.
3. Schedules, DTMF IVR, announcements, voicemail, PSTN, and SIP destinations.
4. Notification and decision webhooks with durable delivery.
5. Routing UI, execution traces, diagnostics, and version rollback.
6. Provider parity, load testing, and production hardening.

## Test Requirements

- Existing-route migration produces identical behavior.
- Concurrent claims produce exactly one winner.
- Restart during IVR, queue, offer, transfer, and webhook delivery recovers.
- Caller hangup cancels outstanding offers, timers, and media work.
- Flow validation rejects cycles, dangling paths, unsupported nodes, and
  excessive timeout budgets.
- Project boundaries hold across routes, flows, destinations, events, and
  recordings.
- No secrets or credentials reach UI responses, events, or logs.
- Twilio, Telnyx, and Plivo pass a shared adapter contract suite using
  realistic callback sequences.
- Load tests cover fast inbound callbacks and ring-group claim contention.

## Acceptance Criteria

- Existing single-agent routes continue to work without reconfiguration.
- A number can route through schedules and DTMF to multiple ring groups.
- One and only one agent can claim a simultaneous offer.
- Unanswered calls follow configured overflow and voicemail paths.
- A notification webhook receives durable lifecycle data without delaying the
  call.
- Flow simulation explains the selected route before publication.
- Active calls expose their pinned flow version and routing trace.
- All data and management operations remain strictly project-scoped.
