// Package tools assembles the MCP tool surface the agent calls. Every
// tool closes over a *episode.Manager — the manager owns DB I/O,
// scenario lookup, and termination decisions; tools just translate
// args and shape return values.
package tools

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"

	"github.com/apteva/apps/mcp/robot/episode"
	"github.com/apteva/apps/mcp/robot/world"
)

func Build(mgr *episode.Manager) []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "list_scenarios",
			Description: "List available navigation scenarios. Returns id, name, description, optimal_steps, max_steps, observability for each.",
			InputSchema: emptyObject(),
			Handler:     listScenarios(mgr),
		},
		{
			Name:        "start_episode",
			Description: "Begin a new episode against a scenario. Args: scenario_id (required), model (optional label for the agent).",
			InputSchema: object(props{
				"scenario_id": stringProp("Scenario id from list_scenarios"),
				"model":       stringProp("Optional label for the agent attempting the run"),
			}, []string{"scenario_id"}),
			Handler: startEpisode(mgr),
		},
		{
			Name:        "observe",
			Description: "Return the agent's current view of the world. Default partial observability — see scenario for radius. Args: episode_id? (defaults to most recent).",
			InputSchema: object(props{
				"episode_id": stringProp("Defaults to most recent episode"),
			}, nil),
			Handler: observe(mgr),
		},
		{
			Name:        "move",
			Description: "Move one cell in a cardinal direction. Args: direction (N|E|S|W), episode_id? (defaults to most recent). Counts as one step regardless of outcome — walls block, position stays.",
			InputSchema: object(props{
				"direction":  enumProp("Direction", []string{"N", "E", "S", "W"}),
				"episode_id": stringProp("Defaults to most recent episode"),
			}, []string{"direction"}),
			Handler: move(mgr),
		},
		{
			Name:        "pick",
			Description: "Attempt to pick up an item at the agent's current cell. Inert in v0.1 (no scenarios ship items); reserved in the contract for v0.2. Args: episode_id?.",
			InputSchema: object(props{
				"episode_id": stringProp("Defaults to most recent episode"),
			}, nil),
			Handler: pick(mgr),
		},
		{
			Name:        "drop",
			Description: "Attempt to drop the held item at the agent's current cell. Inert in v0.1; reserved for v0.2. Args: episode_id?.",
			InputSchema: object(props{
				"episode_id": stringProp("Defaults to most recent episode"),
			}, nil),
			Handler: drop(mgr),
		},
		{
			Name:        "episode_status",
			Description: "Return per-episode metrics — steps, optimal_steps, optimality_ratio, success, terminal_reason. Args: episode_id? (defaults to most recent).",
			InputSchema: object(props{
				"episode_id": stringProp("Defaults to most recent episode"),
			}, nil),
			Handler: episodeStatus(mgr),
		},
	}
}

func listScenarios(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
		scens := mgr.Scenarios()
		out := make([]map[string]any, 0, len(scens))
		for _, s := range scens {
			out = append(out, map[string]any{
				"id":            s.ID,
				"name":          s.Name,
				"description":   s.Description,
				"max_steps":     s.MaxSteps,
				"optimal_steps": s.OptimalSteps,
				"observability": s.Observability,
				"grid":          map[string]int{"width": s.Grid.Width, "height": s.Grid.Height},
			})
		}
		return map[string]any{"scenarios": out}, nil
	}
}

func startEpisode(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		scenID, _ := args["scenario_id"].(string)
		if scenID == "" {
			return nil, errors.New("scenario_id required")
		}
		model, _ := args["model"].(string)
		return mgr.Start(scenID, model)
	}
}

func observe(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		id, _ := args["episode_id"].(string)
		return mgr.Observe(id)
	}
}

func move(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		id, _ := args["episode_id"].(string)
		dir, _ := args["direction"].(string)
		switch world.Direction(dir) {
		case world.North, world.East, world.South, world.West:
			return mgr.Move(id, world.Direction(dir))
		}
		return nil, fmt.Errorf("direction must be one of N|E|S|W (got %q)", dir)
	}
}

func pick(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		id, _ := args["episode_id"].(string)
		return mgr.Pick(id)
	}
}

func drop(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		id, _ := args["episode_id"].(string)
		return mgr.Drop(id)
	}
}

func episodeStatus(mgr *episode.Manager) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		id, _ := args["episode_id"].(string)
		return mgr.Status(id)
	}
}

// --- JSON-schema sugar -----------------------------------------------------

type props map[string]any

func emptyObject() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func object(p props, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": map[string]any(p),
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func enumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}
