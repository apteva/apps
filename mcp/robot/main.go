// Apteva Robot app — agent navigation eval sandbox.
//
// 2D grid worlds. The agent perceives partially via observe(), moves
// cardinally via move(), and the harness ends each episode on goal
// reach (success) or step-cap (timeout). Episode logs and metrics
// (steps, optimal_steps, optimality_ratio) make runs comparable
// across models — the eval harness, not the agent, decides outcomes.
//
// v0.1 ships reach_goal scenarios; pick/drop are reserved in the
// contract for v0.2's items. The MCP tool surface is intended to stay
// stable across world implementations (sim → continuous → hardware).
package main

import (
	"errors"
	"os"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"

	"github.com/apteva/apps/mcp/robot/episode"
	"github.com/apteva/apps/mcp/robot/tools"
	"github.com/apteva/apps/mcp/robot/web"
	"github.com/apteva/apps/mcp/robot/world"
)

const manifestYAML = `schema: apteva-app/v1
name: robot
display_name: Robot
version: 0.1.0
description: Agent navigation eval sandbox. 2D grid worlds; partial observability; harness-decided termination (success or timeout).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: list_scenarios, description: "List available navigation scenarios." }
    - { name: start_episode,  description: "Begin a new episode against a scenario." }
    - { name: observe,        description: "Return the agent's current view of the world." }
    - { name: move,           description: "Move one cell in a cardinal direction." }
    - { name: pick,           description: "Pick up an item (inert in v0.1)." }
    - { name: drop,           description: "Drop the held item (inert in v0.1)." }
    - { name: episode_status, description: "Return per-episode metrics." }
  ui_panels:
    - slot: project.page
      label: Robot
      icon: navigation
      entry: /ui/RobotPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/robot
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/robot.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	mgr *episode.Manager
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("robot app requires a db block")
	}
	scens, err := world.LoadAll(scenariosDir())
	if err != nil {
		return err
	}
	a.mgr = episode.NewManager(ctx.AppDB(), scens)
	ctx.Logger().Info("robot mounted", "scenarios", len(scens))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	if a.mgr == nil {
		return nil
	}
	return web.Build(a.mgr)
}

func (a *App) MCPTools() []sdk.Tool {
	if a.mgr == nil {
		return nil
	}
	return tools.Build(a.mgr)
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// scenariosDir resolves where scenarios/*.json live. The platform's
// source-installer sets APTEVA_SCENARIOS_DIR to the absolute path
// inside the cloned source tree; dev runs fall back to "scenarios/"
// relative to the binary's CWD.
func scenariosDir() string {
	if v := os.Getenv("APTEVA_SCENARIOS_DIR"); v != "" {
		return v
	}
	return "scenarios"
}

func main() { sdk.Run(&App{}) }
