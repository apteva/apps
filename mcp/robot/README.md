# robot

Agent navigation eval sandbox. v0.1 ships 2D grid worlds; the agent
perceives partially via `observe()`, moves cardinally via `move()`,
and the harness terminates each episode on goal-reach (success) or
step-cap (timeout). Episode logs and metrics (`steps`, `optimal_steps`,
`optimality_ratio`) make runs comparable across models.

## Tool surface

The MCP contract is intended to stay stable across world implementations
(simulator → continuous-pose sim → real hardware bridge). The agent
sees the same five gameplay tools regardless of what's underneath.

| Tool | Purpose |
|---|---|
| `list_scenarios` | Discovery |
| `start_episode` | Begin a run; returns `episode_id` + initial observation |
| `observe` | Egocentric view + goal bearing + step count + status |
| `move(N\|E\|S\|W)` | One cardinal step; counts as a step regardless of outcome |
| `pick` / `drop` | Inert in v0.1; reserved for v0.2's items |
| `episode_status` | Per-episode metrics |

`give_up` is intentionally not in the surface — quitting is not an
agent decision. The harness ends episodes on success or timeout.

## Building

```sh
cd apps/mcp/robot
go build .
```

Local run (no platform):

```sh
APTEVA_GATEWAY_URL=http://localhost:5280 \
APTEVA_APP_TOKEN=dev-token \
APTEVA_INSTALL_ID=0 \
DB_PATH=/tmp/robot-dev.db \
APTEVA_SCENARIOS_DIR=$(pwd)/scenarios \
APTEVA_UI_DIR=$(pwd)/ui \
go run .
```

Then POST a `start_episode` to `http://127.0.0.1:8080/mcp` (with the
matching `Authorization: Bearer dev-token`) or hit the panel through
the platform's reverse proxy at `/api/apps/robot/`.

## Scenarios

Drop a JSON file into `scenarios/`. Required fields: `id`, `grid`,
`agent_start`, `goal`, `max_steps`, `success: {type: "reach_goal"}`.
Validation runs at sidecar boot — malformed scenarios fail loud, not
silent.

The optimal step count (A*, 4-connected) is computed at load time and
stored on the scenario; `optimality_ratio = optimal_steps / steps` is
written to every successful episode.

## Roadmap

| Version | Adds |
|---|---|
| **v0.1** | Grid, single agent, partial-obs default, A* baseline, success/timeout termination |
| v0.2 | Items + delivery scenarios, hazards (third terminal reason) |
| v0.3 | Continuous 2D + heading + speed |
| v0.4 | Multi-agent episodes |
| v1.0 | Hardware adapter — same MCP tools, real-world backend |
