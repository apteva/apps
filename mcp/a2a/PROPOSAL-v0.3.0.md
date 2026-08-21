# A2A v0.3.0 Proposal: Generic Node Federation

Status: app-only MVP implemented; Fleet automation deferred

## Summary

A2A v0.3.0 extends the existing same-project task ledger into a generic,
standards-compatible federation layer. It deliberately does not model
`hub`, `tenant`, `fleet`, `server`, or `public` as protocol scopes. Every
Apteva installation is an A2A node, every remotely callable agent is
described by an A2A Agent Card, and access is determined by grants from a
resource-owning node to a peer node.

Fleet may automate pairing between nodes it provisions, but it is not in
the discovery or message-delivery path. The same pairing and authorization
model works for any two manually configured Apteva A2A installations. Direct
third-party Agent Card registration is a follow-up extension.

The current local MCP tools remain the agent-facing interface. Agents call
`agents_discover`, optional `agent_get`, `agent_send`, `agent_ask`,
`agent_reply`, and `agent_tasks`; the A2A app translates remote operations to
and from the A2A 1.0 protocol.

`agents_discover` searches all configured sources and returns lightweight but
immediately actionable results. An agent may pass the returned `address`
directly to `agent_send` or `agent_ask`. `agent_get` is an optional inspection
step for retrieving the selected agent's complete current Agent Card and
capability details.

## Goals

- Let a local agent discover authorized local and remote Agent Cards without
  knowing deployment topology.
- Keep broad discovery compact while making every result immediately
  actionable; detailed Agent Card inspection remains optional.
- Let paired Apteva nodes communicate directly using standard A2A task and
  message operations.
- Keep discovery, credentials, URLs, and protocol details out of agent
  prompts and tool arguments.
- Use generic peer identity and target-side capability grants instead of
  topology-specific visibility flags.
- Allow Fleet to establish and reconcile relationships automatically while
  preserving the same manual pairing path for any two nodes.
- Preserve the current local task experience, including asynchronous replies,
  thread routing, rate limits, and the dashboard ledger.

## Non-goals

- A global public agent search engine.
- Transitive trust between peers.
- Routing all remote traffic through a controller or Fleet.
- Treating Apteva's directory API, node identity, or locator strings as part
  of the A2A standard.
- Requiring an external A2A implementation to understand Apteva concepts.
- Direct registration of third-party Agent Card URLs in the v0.3.0 MVP.

## Design principles

### Actionable discovery with optional inspection

`agents_discover` searches local agents and configured peers and returns a
compact list whose addresses are already accepted by `agent_send` and
`agent_ask`. `agent_get` optionally resolves one address to its complete
current Agent Card.

The agent does not list nodes, manage credentials, call peer directory URLs,
or fetch raw HTTP resources itself. This retains the list/detail separation
used by Hermes and the OpenClaw A2A plugin without forcing the detail call
when the discovery summary already provides enough information.

### A2A boundary

The standards-facing boundary consists of Agent Cards, their advertised
security schemes and interfaces, A2A messages, tasks, artifacts, and task
lifecycle operations. Apteva-specific discovery and pairing happen outside
that boundary.

The canonical remote target is the Agent Card and its advertised interface
URL. Candidate references returned by `agents_discover` and actionable
addresses returned by `agent_get` are opaque local handles only.

### Generic node identity

Each A2A installation owns a stable node identity:

```text
node_id       opaque UUID/ULID
display_name  operator-facing name
base_url      current public/control URL
```

The identity belongs to the A2A installation, not to a Fleet tenant record.
Migration preserves it. Cloning must generate a new node identity and key.

### Target-side authorization

The node hosting an agent decides who may discover or invoke it. A peer never
authorizes itself by placing capabilities in a request token.

```text
subject   configured peer node ID
actions   discover, invoke
resource  agent ID, explicit set of agents, or all agents
```

There is no `exposure=fleet` property. A card is visible to a peer exactly
when its `discover_agents` grant matches. Invocation is checked separately
against `invoke_agents`, so card visibility does not automatically imply
permission to send work. Protocol task reads, follow-ups, and cancellation
also require that invocation grant and are limited to tasks owned by that
peer.

Trust is never transitive. Pairing node A with node B does not expose B's
other peers to A.

## Data model

The existing task and message tables remain and receive protocol correlation
columns. New migrations add the following conceptual tables.

### Node identity

```text
a2a_node
  node_id
  display_name
  created_at
```

### Peers and grants

For the v0.3.0 MVP, peers and their target-side grants live in the encrypted
`peers_json` app install configuration. Each entry contains a peer ID, display
name, base URL, unique bearer token, `discover_agents`, and `invoke_agents`.
The grant lists accept `*`, a local agent name, local agent ID, or opaque card
ID. Empty grant lists deny the corresponding action.

A database-backed peer editor, token rotation workflow, collections, and
direct third-party card registry are follow-up work; they are not required for
manual Apteva-to-Apteva pairing.

### Agent identities and cards

```text
a2a_agent_profiles
  project_id
  local_agent_id
  card_id
  description
  skills_json
  enabled
  created_at
  updated_at
```

`card_id` is stable and opaque. The card is generated from current platform
agent information plus A2A-owned metadata. Numeric platform agent IDs are not
published as federation identities.

Remote discovery summaries and resolved cards are cached in
`a2a_remote_agents` with a bounded expiry. The cache stores app-internal
routing metadata behind the opaque address returned to agents.

### Remote task correlation

The task ledger gains:

```text
protocol_task_id
protocol_context_id
direction             local, inbound, outbound
peer_id
remote_task_id
remote_context_id
remote_card_id
sync_state
last_synced_at
```

Existing integer task IDs remain local UI/tool handles for compatibility.

## Node pairing and authentication

### Pairing

Pairing is an administrative, out-of-band operation. In v0.3.0 the operator
configures a peer ID, display name, base URL, and unique bearer token on each
side, then installs independent target-side discovery and invocation grants.
A future administrative API may package the same values into a short-lived,
single-use invitation for automated provisioning.

Pairing does not grant access by itself.

### Request authentication

v0.3.0 uses a distinct high-entropy bearer token for every configured peer,
matching the practical model used by Hermes and the OpenClaw A2A plugin. The
destination maps the token to a peer identity using constant-time comparison,
then evaluates its own grants. Nothing in the request body may assert the
calling peer's identity or permissions.

Agent Cards advertise the bearer security scheme. Third-party A2A
cards may advertise OAuth2, OIDC, API-key, or other standard schemes; direct
card connections use the scheme declared by that card rather than Apteva peer
tokens.

## Discovery

### Agent-facing MCP tool

Replace topology scopes with optional content and source filters:

```json
{
  "query": "support",
  "peer": "optional-peer-id-or-alias",
  "capability": "optional-skill-or-tag",
  "limit": 50
}
```

With no `peer`, discovery searches authorized local summaries and every
configured healthy Apteva peer.
`peer` is a generic optional source filter, not a topology or authorization
scope.

The result is a lightweight candidate list:

```json
{
  "agents": [
    {
      "address": "opaque-actionable-handle",
      "name": "Customer Support",
      "description": "Handles customer support operations",
      "peer": "Acme Operations",
      "skills": ["order-status"]
    }
  ]
}
```

No credentials are returned to the agent.

### Optional Agent Card inspection tool

The agent may inspect a candidate before invoking it:

```json
{
  "agent": "opaque-actionable-handle",
  "refresh": false
}
```

`agent_get` resolves or refreshes the canonical Agent Card and returns richer
details when the discovery summary is insufficient:

```json
{
  "address": "opaque-actionable-handle",
  "name": "Customer Support",
  "description": "Handles customer support operations",
  "peer": "Acme Operations",
  "online": true,
  "skills": [
    {
      "id": "order-status",
      "name": "Order status",
      "description": "Finds and explains the current state of an order"
    }
  ],
  "input_modes": ["text/plain"],
  "output_modes": ["text/plain"]
}
```

The returned `address` can be passed directly to `agent_send` or
`agent_ask`. Credentials, raw authentication configuration, and internal
platform IDs remain hidden.

### Apteva directory extension

Every paired Apteva node may expose:

```text
GET /api/apps/a2a/directory/agents
GET /api/apps/a2a/agent-cards/{card_id}
```

These routes authenticate the calling node, evaluate `discover_agents`, and
return authorized summaries or a standard Agent Card. Query parameters may
filter by text or skill but cannot widen the caller's grants.

The directory is an Apteva extension, not an A2A protocol endpoint. Direct
third-party Agent Card URLs and public well-known discovery remain follow-up
work.

### Discovery execution

1. A local agent calls `agents_discover`, optionally with a text, capability,
   or generic peer filter.
2. The local app builds the source set from local agents and every configured
   peer.
3. It searches local cards and queries peer directories concurrently using
   app-owned credentials.
4. Each peer independently filters its cards using target-side grants.
5. The app caches the routing metadata needed to invoke each result, merges
   and ranks summaries, and returns actionable opaque addresses.
6. The agent may call `agent_get` for a selected address. If it does, the app
   obtains the complete card from cache or the authoritative source, validates
   it, and rechecks discovery authorization.

Unavailable peers do not fail the whole search. Results identify stale or
unreachable sources in a `warnings` field.

## Communication

### Outbound request

1. The agent calls `agent_ask(to, message)` with the actionable address
   returned by `agents_discover` (or echoed by optional `agent_get`).
2. The local app resolves the canonical Agent Card and preferred interface.
3. It authenticates using the card's declared security scheme.
4. It invokes the A2A 1.0 JSON-RPC method `message/send` on the card's
   advertised interface.
5. It stores the returned remote task and context IDs in the local ledger.
6. It returns immediately to the agent, preserving the current asynchronous
   tool contract.

### Inbound request

1. The remote node calls the interface URL for one local card.
2. The app authenticates the peer and checks `invoke_agents` for that card.
3. It creates an inbound ledger task with a protocol UUID.
4. It injects the existing trust-bounded `[a2a]` event into the local agent.
5. It returns an A2A task in `TASK_STATE_SUBMITTED` or
   `TASK_STATE_WORKING`.

### Reply and synchronization

The receiving local agent continues to call `agent_reply`. That updates the
inbound A2A task; it does not create an unrelated reverse request.

For v0.3.0, the origin node synchronizes outstanding tasks with `GetTask` on a
bounded backoff schedule. On changes, it updates its outbound task record and
delivers an event to the original requesting thread. A later release may prefer
`SubscribeToTask` or task push notifications when advertised by the card.

Follow-ups use `SendMessage` with the existing remote task ID. Cancellation
maps to `CancelTask`. One-way `agent_send` uses `SendMessage` but does not keep
an active reply subscription after acknowledgement.

## MCP tool compatibility

- `agent_send`, `agent_ask`, `agent_reply`, and `agent_tasks` retain their
  current names and local behavior.
- `agent_get` is added as an optional detail step and echoes the actionable
  address accepted by `agent_send` and `agent_ask`.
- `to` accepts both existing `agent:<local-id>` values and new opaque
  actionable addresses returned by `agents_discover`.
- `agents_discover` removes `server`, `fleet`, `web`, `project`, and `all`
  scope semantics. Its optional `peer` argument is only a generic source
  filter.
- During one compatibility release, an old `scope` argument may be accepted
  but ignored with a deprecation note.

## A2A app changes

A2A v0.3.0 owns the core implementation:

- Stable local node identity and operator-facing node metadata.
- Encrypted peer configuration and target-side discovery/invocation grants.
- Stable card IDs and Agent Card generation.
- Authenticated Apteva directory routes.
- A discovery aggregator that fans out concurrently, ranks and deduplicates
  lightweight results, and returns actionable opaque addresses.
- An `agent_get` implementation that resolves and validates the selected
  Agent Card and returns an actionable opaque address.
- Standards-compatible A2A HTTP/JSON or JSON-RPC server routes.
- Outbound A2A client selected from each card's supported interfaces.
- Inbound authoritative tasks, outbound task correlation, polling worker, and
  restart-safe persistence.
- Existing local pair rate limits plus authenticated remote protocol routes.

The federation-facing routes are declared `no_auth`; the handler validates
peer or card-declared authentication itself. Existing operator task routes
remain platform-authenticated.

## Fleet app changes — deferred

No Fleet files are changed in v0.3.0. Manual reciprocal `peers_json`
configuration is sufficient to run and test the A2A app. Fleet changes would
only be required later for zero-touch installation and pairing; Fleet is never
required for A2A protocol traffic.

Fleet should:

- Declare/bind the controller's A2A app as a provisioning dependency.
- After a tenant becomes active, ensure the tenant has a compatible A2A app
  installed.
- Run the generic pairing workflow using administrative APIs already available
  to Fleet.
- Apply an operator-selected grant template on each side. Fleet-specific
  template names may exist in Fleet configuration, but they produce ordinary
  A2A grants and never appear in the A2A protocol.
- Add `tenant_a2a_pair`, `tenant_a2a_reconcile`, and
  `tenant_a2a_unpair` operations.
- Reconcile peer URLs and health after tenant connect, start, and migration.
- Revoke the controller-side peer and grants when a tenant is destroyed or
  disconnected.
- Preserve node identity during migration.
- Force a new node identity and key after clone, before the clone is paired;
  copying the source A2A identity would impersonate the source node.
- Surface pairing/health state in tenant inventory without exposing private
  keys or request credentials.

Normal discovery and task traffic goes directly between A2A apps. Fleet is
not a proxy or message relay.

## Server changes

### Required for v0.3.0 private federation: none

The existing platform already provides the primitives needed for the first
release:

- App `no_auth` HTTP routes that perform their own authentication.
- Preservation of the caller's original Authorization header on those routes.
- Reverse proxying under `/api/apps/a2a/...`.
- Encrypted app install configuration and app restart on config changes.
- Platform callbacks for agent listing and local event/thread delivery.

Node identity, peer verification, grants, cards, and protocol handling remain
inside the A2A sidecar. Federation should use one global A2A install per node
to keep the directory and node identity unambiguous.

### Optional follow-up server changes

Server work is justified later for:

- A root-level `/.well-known/agent-card.json` mapping for public A2A discovery.
  App-prefixed private directories and directly configured card URLs do not
  require this route.
- A platform-wide stable node identity if other apps need the same federation
  primitive. v0.3.0 should not block on generalizing it.
- A richer platform secret-store API if A2A later needs dynamically generated
  credentials beyond encrypted install configuration.
- Richer agent skill/profile metadata if cards need more than A2A-owned
  descriptions and skills.

These should not be bundled into the private node-federation MVP unless strict
public well-known conformance is part of the release requirement.

## Security requirements

- TLS is mandatory for non-loopback peers.
- Peer bearer tokens are unique, high entropy, stored through encrypted app
  install configuration, and compared in constant time.
- A token maps to exactly one configured peer identity; request bodies cannot
  override it.
- Future pairing invitations must be single-use, short-lived, and auditable.
- Discovery and invocation are separately authorized.
- Every task is authorized against its concrete target card even if the card
  was previously cached.
- Grant revocation takes effect immediately for new calls.
- Peer directory responses never contain credentials or internal numeric
  platform IDs.
- Remote content retains the existing prompt-injection trust footer.
- Clone credential rotation is mandatory before the clone is connected.
- Outbound URLs are validated against the paired peer/card to prevent SSRF and
  credential forwarding to redirected hosts.

## Version and rollout

Proposed app version: `0.3.0`.

1. Add node identity, cards, encrypted peer configuration, grants, and
   migrations. **Implemented.**
2. Add authenticated directory routes and tests between two in-memory nodes.
   **Implemented.**
3. Add inbound A2A task handling and local agent delivery. **Implemented.**
4. Add outbound client, task correlation, and synchronization worker.
   **Implemented.**
5. Pair one controller and one non-production tenant; verify discovery,
   request, input-required follow-up, completion, cancellation, revocation,
   restart, and migration.
6. Later: add operator peer/grant UI and Fleet pairing/reconciliation hooks.

## Acceptance criteria

- Two generic Apteva nodes can pair without either declaring itself a hub or
  tenant.
- A grant on node B allows node A to discover exactly the selected cards and
  no others.
- Discovery has no topology scope argument and merges local and remote cards.
- `agents_discover` returns authorized matching candidates across local and
  configured remote sources in one compact, immediately actionable list.
- Every discovery address can be passed directly to `agent_send` or
  `agent_ask` without calling `agent_get` first.
- Optional `agent_get` returns the complete selected Agent Card without
  requiring the agent to call a remote directory or raw URL itself.
- A local agent can ask a remote agent and receive the completion in its
  originating thread.
- Input-required follow-ups and cancellation use the same remote A2A task.
- Removing a card from `discover_agents` removes it from fresh discovery.
- Removing a card from `invoke_agents` blocks invocation even when its card
  is cached.
- An unconfigured peer or peer with an invalid bearer token receives no card
  or task information.
- The existing same-project A2A behavior and ledger UI continue to work.
