# Agent to Agent

The A2A app gives Apteva agents one tool surface for local and remote
communication. It preserves the local task ledger and live-thread delivery,
and adds authenticated A2A JSON-RPC transport between configured Apteva
installations.

Install one global A2A app per participating Apteva installation. That app is
the installation's A2A node: it owns the stable node ID, generates cards for
local attached agents, stores inbound authoritative tasks and outbound task
records, and communicates with configured peer installations.

## Agent flow

1. `agents_discover` searches local agents and every configured peer.
2. Every returned `address` is immediately accepted by `agent_send` and
   `agent_ask`.
3. `agent_get` is optional and returns the complete current Agent Card.
4. Remote replies are synchronized into the originating local thread.

Agents never receive peer credentials or raw routing configuration.

## Manual peer configuration

Fleet automation is intentionally not required. Configure each participating
global A2A app with a reciprocal entry in the encrypted `peers_json` install
setting:

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

## HTTP surface

Peer-authenticated routes:

```text
GET  /api/apps/a2a/directory/agents
GET  /api/apps/a2a/agent-cards/{card_id}
POST /api/apps/a2a/agents/{card_id}
```

The JSON-RPC endpoint supports:

```text
message/send
tasks/get
tasks/cancel
```

Agent Cards advertise JSON-RPC with streaming and push notifications disabled.
The origin app polls open remote tasks and delivers state changes through the
existing local A2A event mechanism.

## Tests

Run deterministic app tests with `go test ./...`. Run the real-agent suite
with the topology-capable CLI from this app directory:

```sh
apteva test ./scenarios
```

The suite covers same-project communication, global-install project isolation,
two-node request/reply and follow-up flows, peer grants, a hub connected to two
independent tenant nodes, and a Fleet-style tenant agent contacting a main-node
agent.
