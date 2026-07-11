package main

// sims_input / sims_logs / sims_stream_url tool bodies. Input + logs
// are also reachable over the live-stream WebSocket, but exposing them
// as discrete tools lets agents drive a device headlessly (tap through
// a flow, read the log) without holding a stream open.

import (
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── sims_input ─────────────────────────────────────────────────────

func (a *App) toolSimsInput() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_input",
		Description: "Forward a tap / swipe / key / text event to a booted sim. Coordinates are normalized 0..1 against the device screen.",
		InputSchema: schemaObject(map[string]any{
			"sim_id": map[string]any{"type": "string", "description": "Required. Target sim."},
			"kind":   map[string]any{"type": "string", "enum": []string{"tap", "swipe", "key", "text"}, "description": "Required."},
			"x":      map[string]any{"type": "number", "description": "tap/swipe start X (0..1)."},
			"y":      map[string]any{"type": "number", "description": "tap/swipe start Y (0..1)."},
			"x2":     map[string]any{"type": "number", "description": "swipe end X (0..1)."},
			"y2":     map[string]any{"type": "number", "description": "swipe end Y (0..1)."},
			"ms":     map[string]any{"type": "number", "description": "swipe duration in ms."},
			"key":    map[string]any{"type": "string", "description": "logical key: BACK|HOME|APP_SWITCH|ENTER|DEL (android); HOME|ENTER|DEL (ios)."},
			"text":   map[string]any{"type": "string", "description": "literal text to type."},
		}, []string{"sim_id", "kind"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			if simID == "" {
				return nil, errors.New("sim_id required")
			}
			sim, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if sim == nil || sim.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted", simID)
			}
			ev := inputEvent{
				Kind: strArg(args, "kind"),
				X:    floatArg(args, "x"), Y: floatArg(args, "y"),
				X2: floatArg(args, "x2"), Y2: floatArg(args, "y2"),
				DurationMS: int(floatArg(args, "ms")),
				Key:        strArg(args, "key"), Text: rawStringArg(args, "text"),
			}
			if err := validateInputEvent(ev); err != nil {
				return nil, err
			}
			switch sim.Platform {
			case "android":
				err = androidSendInput(sim.Serial, ev)
			case "ios":
				err = a.iosSendInput(sim.ID, ev)
			default:
				err = fmt.Errorf("unknown platform %q", sim.Platform)
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

// ─── sims_logs ──────────────────────────────────────────────────────

func (a *App) toolSimsLogs() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_logs",
		Description: "Tail the device console (logcat on android, the unified log on ios).",
		InputSchema: schemaObject(map[string]any{
			"sim_id": map[string]any{"type": "string", "description": "Required. Target sim."},
			"lines":  map[string]any{"type": "number", "description": "Max lines to return. Default 200."},
		}, []string{"sim_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			if simID == "" {
				return nil, errors.New("sim_id required")
			}
			sim, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if sim == nil {
				return nil, fmt.Errorf("sim %q not known", simID)
			}
			lines := normalizeLogLines(int(floatArg(args, "lines")))
			var content string
			switch sim.Platform {
			case "android":
				content, err = androidLogs(sim.Serial, lines)
			case "ios":
				content, err = iosLogs(sim.ID, lines)
			default:
				err = fmt.Errorf("unknown platform %q", sim.Platform)
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"content": content}, nil
		},
	}
}

// ─── sims_stream_url ────────────────────────────────────────────────

func (a *App) toolSimsStreamURL() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_stream_url",
		Description: "Mint a fresh short-lived WebSocket URL for live screen streaming + input on a booted sim. Existing URLs for the same sim remain valid until their own expiry.",
		InputSchema: schemaObject(map[string]any{
			"sim_id": map[string]any{"type": "string", "description": "Required. Target sim."},
		}, []string{"sim_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			if simID == "" {
				return nil, errors.New("sim_id required")
			}
			sim, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if sim == nil || sim.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted", simID)
			}
			if err := streamingCapabilityCheckFor(ctx, sim.Platform); err != nil {
				return nil, err
			}
			stream, err := dbMintStreamToken(ctx.AppDB(), simID, 1*time.Hour)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"stream_url": a.streamURL(ctx, simID, stream.WSToken),
				"expires_at": stream.ExpiresAt,
			}, nil
		},
	}
}

// floatArg pulls a numeric field, returning 0 on miss. JSON numbers
// decode as float64 through the MCP path.
func floatArg(args map[string]any, k string) float64 {
	switch v := args[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}
