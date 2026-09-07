# Agent to Agent

The A2A app gives Apteva agents one tool surface for local and remote
communication. It preserves the local task ledger and live-thread delivery,
and adds A2A JSON-RPC transport between configured Apteva installations and
public third-party agents.

Install one global A2A app per participating Apteva installation. That app is
the installation's A2A node: it owns the stable node ID, generates cards for
local attached agents, stores inbound authoritative tasks and outbound task
records, and communicates with configured peer installations.

## Agent flow

1. `agents_discover` searches local agents and every configured connection.
   Passing `card_url` also discovers a public Agent Card directly in the same
   call and caches it as a connection.
2. Every returned `address` is immediately accepted by `agent_send` and
   `agent_ask`.
3. `agent_get` is optional and returns the complete current Agent Card.
4. Remote replies are synchronized into the originating local thread.

Agents never receive peer credentials or raw routing configuration.

## Connections and peer registry

Every remote installation is a generic peer in `a2a_peers`; there are no
Fleet-, hub-, or tenant-specific peer types. Peer credentials are encrypted
with the installation's A2A key before they are written to the app database.
Set `A2A_MASTER_KEY` to a base64-encoded 32-byte key to provide that key from a
secret manager; otherwise A2A creates `a2a-peer.key` beside its database.

The project panel's **Connections** view adds either:

- a public Agent Card URL, with an optional outbound bearer token; or
- another Apteva A2A node, with its reciprocal pairing token and grants.

Both use the existing peer and remote-card cache tables. Public cards are
validated before storage, must use HTTPS, cannot redirect, and cannot target a
private network. Public-agent HTTP connections resolve and check the destination
at dial time and connect directly to that checked IP. For local test servers
only, set `allow_loopback_public_agents=true`; it defaults to false and does not
permit other private networks. An anonymous public card never becomes an inbound
credential. Agent discovery preserves operator-supplied connection credentials.

Node connections added in the panel default to no inbound access. Choose explicit
discovery and invocation grants; `*` explicitly grants all exposed agents across
the installation. Remote access requires a current A2A attachment, including on
platforms that do not report attachment metadata (which fail closed).

Bound apps can reconcile relationships without exposing administrative tools
to agents:

```text
node_info
peer_upsert
peer_remove
```

These operations are `app_only`. A peer created through `peer_upsert` is owned
by the authenticated calling app install, and no other app install can update
or remove it. This lets a lifecycle controller such as Fleet pair nodes while
A2A remains the direct discovery and message transport.

## Manual peer configuration

Automation is not required. Configure each participating global A2A app with a
reciprocal entry in the encrypted `peers_json` install setting:

```json
[
  {
    "id": "main",
    "name": "Main instance",
    "base_url": "https://agents.example.com/api/apps/a2a",
    "token": "a-unique-high-entropy-shared-token",
    "discover_agents": ["Support"],
    "invoke_agents": ["Support"]
  }
]
```

`discover_agents` and `invoke_agents` are target-side grants for requests
arriving from this peer. Entries may contain `"*"`, a local agent name, a
local numeric agent ID, or the generated opaque card ID. Empty lists deny the
action.

Each relationship should use a different token. HTTPS is required except for
loopback development URLs.

`peers_json` remains a backwards-compatible desired-state input for
operator-managed peers. At mount, A2A imports and reconciles those entries into
the same registry without overwriting peers owned by an app install.

## HTTP surface

Peer-authenticated routes:

```text
GET  /api/apps/a2a/directory/agents
GET  /api/apps/a2a/agent-cards/{card_id}
POST /api/apps/a2a/agents/{card_id}
```

Apteva-node JSON-RPC supports A2A 1.0 `SendMessage`, `GetTask`, and
`CancelTask`, including the wrapped `SendMessage` response. The historical
`message/send`, `tasks/get`, and `tasks/cancel` aliases remain for existing
Apteva nodes. Outbound node calls fall back only on an explicit method-not-found
response, never on timeouts or ambiguous failures.

Agent Cards advertise JSON-RPC with streaming and push notifications disabled.
The origin app polls open remote tasks and delivers state changes through the
existing local A2A event mechanism.

For third-party cards, the client negotiates from the card: modern A2A v1 uses
`SendMessage` and the `A2A-Version` header; legacy v0.3 cards use the older
wire shape. Synchronous public replies are written immediately to the same
outbound task ledger. Text, structured, and file artifact payloads are retained
with the task, and their content is included in delivered results. Follow-ups
use the same response adapter as initial sends. Following up a terminal outbound
task starts a new tracked task within its existing remote context.

Apteva-to-Apteva one-way sends use the `apteva.one_way` request metadata flag;
the receiver records a completed message without requesting a reply. Other A2A
services may process one-way sends according to their own task semantics.

Reply state, content, and pending delivery are stored atomically. Failed local
notifications retry even after completion. Delivery is at least once: a crash
after the platform accepts an event but before acknowledgement can repeat that
event. Polling selects least-recently-polled tasks, uses four concurrent network
requests with a four-second project budget, and backs failed tasks off for
30–240 seconds. Directory refreshes do not renew cached Agent Card lifetimes.

## Tests

Run deterministic app tests with `GOWORK=off go test -mod=readonly -race ./...`.
From the apps repository root, run `bun install --frozen-lockfile` and
`bun run test:a2a-ui` for the React interaction tests. Run the real-agent suite
with the topology-capable CLI from this app directory:

```sh
apteva test ./scenarios
```

The suite covers same-project communication, global-install project isolation,
two-node request/reply and follow-up flows, peer grants, a hub connected to two
independent tenant nodes, a Fleet-style tenant agent contacting a main-node
agent, and an Apteva agent talking to a live public third-party A2A agent.
