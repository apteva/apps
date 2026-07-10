package main

// MCP tool registry. Bodies that need significant code live in their
// own files (capabilities.go, android_*.go, ios_*.go); this file
// declares the schemas + thin handlers that translate raw arg maps
// into typed inputs and back into JSON-encodable outputs.
//
// Tool naming: every tool is `sims_<verb>` so they group together in
// the dashboard's tool picker and so the prefix can be allowlisted /
// audited as a unit.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		a.toolSimsCapabilities(),
		a.toolSimsList(),
		a.toolSimsBoot(),
		a.toolSimsShutdown(),
		a.toolSimsScreenshot(),
		a.toolSimsBuild(),
		a.toolSimsInstall(),
		a.toolSimsLaunch(),
		a.toolSimsRun(),
		a.toolSimsInput(),
		a.toolSimsLogs(),
		a.toolSimsStreamURL(),
	}
}

// schemaObject is a small builder for the JSON-schema fragments we
// pass as InputSchema. Mirrors the helper code in apps/mcp/code's
// tools file (kept independent here to avoid cross-app imports).
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// strArg pulls a string field from the raw tool args, returning "" on
// missing or non-string. Tool handlers do their own required-field
// validation rather than relying on JSON-schema enforcement, because
// the SDK's MCP path is permissive.
func strArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func rawStringArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

// projectIDFor resolves the project the call is scoped to. Prefers
// the SDK-managed current project (set by APTEVA_PROJECT_ID for
// project-scoped installs, or per-call _project_id for global-scoped
// calls). Falls back to args["_project_id"] for older platforms that
// don't set it on the context.
func projectIDFor(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if p := ctx.CurrentProject(); p != "" {
		return p, nil
	}
	if v := strArg(args, "_project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when calling from a global scope")
}

func dbGetProjectSim(ctx *sdk.AppCtx, args map[string]any, simID string) (*Sim, error) {
	projectID, err := projectIDFor(ctx, args)
	if err != nil {
		return nil, err
	}
	sim, err := dbGetSim(ctx.AppDB(), simID)
	if err != nil || sim == nil {
		return sim, err
	}
	if sim.ProjectID != projectID {
		return nil, nil
	}
	return sim, nil
}

// ─── sims_capabilities ──────────────────────────────────────────────

func (a *App) toolSimsCapabilities() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_capabilities",
		Description: "Report host-level support for android + ios — which external tools are present, which are missing, and what to install. Call this before sims_boot / sims_run so the UI can disable platforms that won't work.",
		InputSchema: schemaObject(map[string]any{
			"_unused": map[string]any{
				"type":        "boolean",
				"description": "Reserved. sims_capabilities takes no arguments; pass any object.",
			},
		}, nil),
		Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
			return probeCapabilities(ctx), nil
		},
	}
}

// ─── sims_list ──────────────────────────────────────────────────────

func (a *App) toolSimsList() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_list",
		Description: "List sims known to this project (booted + shutdown). Optional platform filter.",
		InputSchema: schemaObject(map[string]any{
			"platform": map[string]any{
				"type":        "string",
				"enum":        []string{"android", "ios"},
				"description": "Optional. Filter by platform.",
			},
		}, nil),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			proj, err := projectIDFor(ctx, args)
			if err != nil {
				return nil, err
			}
			sims, err := dbListSims(ctx.AppDB(), proj)
			if err != nil {
				return nil, err
			}
			if filter := strArg(args, "platform"); filter != "" {
				filtered := sims[:0]
				for _, s := range sims {
					if s.Platform == filter {
						filtered = append(filtered, s)
					}
				}
				sims = filtered
			}
			return map[string]any{"sims": sims}, nil
		},
	}
}

// ─── sims_boot ──────────────────────────────────────────────────────

func (a *App) toolSimsBoot() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_boot",
		Description: "Boot an emulator (android) or Simulator (ios). Auto-creates a device matching the requested image + device_type if none exists. Reuses an already-booted device when one matches.",
		InputSchema: schemaObject(map[string]any{
			"platform": map[string]any{
				"type":        "string",
				"enum":        []string{"android", "ios"},
				"description": "Required. Which backend to boot.",
			},
			"image": map[string]any{
				"type":        "string",
				"description": "Android: sdkmanager system-image identifier. iOS: simctl runtime id. Empty = use install config default.",
			},
			"device_type": map[string]any{
				"type":        "string",
				"description": "Android: avdmanager device profile. iOS: simctl device-type id. Empty = use install config default.",
			},
		}, []string{"platform"}),
		Handler: a.handleSimsBoot,
	}
}

func (a *App) handleSimsBoot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	platform := strArg(args, "platform")
	if err := capabilityCheckFor(ctx, platform); err != nil {
		return nil, err
	}
	image := strArg(args, "image")
	deviceType := strArg(args, "device_type")
	switch platform {
	case "android":
		image = valueOrConfigDefault(ctx, image, "android_image")
		deviceType = valueOrConfigDefault(ctx, deviceType, "android_device_type")
		return a.bootAndroid(ctx, image, deviceType)
	case "ios":
		image = valueOrConfigDefault(ctx, image, "ios_runtime")
		deviceType = valueOrConfigDefault(ctx, deviceType, "ios_device_type")
		return a.bootIOS(ctx, image, deviceType)
	}
	return nil, fmt.Errorf("platform %q invalid", platform)
}

// bootIOS resolves runtime + device_type identifiers, ensures a
// reusable simulator device exists, then boots it. Mirrors
// bootAndroid's idempotent semantics — already-booted returns the
// existing row.
func (a *App) bootIOS(ctx *sdk.AppCtx, runtime, deviceType string) (*Sim, error) {
	a.bootMu.Lock()
	defer a.bootMu.Unlock()

	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	udid, err := ensureIOSDevice(cctx, deviceType, runtime)
	if err != nil {
		return nil, err
	}
	if existing, _ := dbGetSim(ctx.AppDB(), udid); existing != nil && existing.Status == "booted" {
		if a.sup.probeAlive(*existing) {
			return existing, nil
		}
		_ = dbUpdateSim(ctx.AppDB(), udid, map[string]any{"status": "shutdown", "pid": 0})
	}
	if err := a.enforceSimCapacity(ctx, udid); err != nil {
		return nil, err
	}
	resolvedRuntime, _ := resolveIOSRuntime(cctx, runtime)
	return bootIOSSim(ctx, a.sup, udid, deviceType, resolvedRuntime)
}

// bootAndroid: ensure an AVD exists, boot it, return the Sim row.
// Reuses an already-booted sim for the same AVD if found.
func (a *App) bootAndroid(ctx *sdk.AppCtx, image, deviceType string) (*Sim, error) {
	a.bootMu.Lock()
	defer a.bootMu.Unlock()

	// AVD creation legitimately takes longer than listing existing devices.
	// The previous five-second parent context accidentally truncated
	// ensureAVD's own 60-second creation timeout.
	cctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	avd, err := ensureAVD(cctx, deviceType, image)
	if err != nil {
		return nil, err
	}
	if existing, _ := dbGetSim(ctx.AppDB(), avd); existing != nil && existing.Status == "booted" {
		if a.sup.probeAlive(*existing) {
			return existing, nil
		}
		_ = dbUpdateSim(ctx.AppDB(), avd, map[string]any{"status": "shutdown", "pid": 0})
	}
	if err := a.enforceSimCapacity(ctx, avd); err != nil {
		return nil, err
	}
	extraArgs := splitArgs(configOrDefault(ctx, "emulator_extra_args"))
	return bootAndroidSim(ctx, a.sup, avd, deviceType, image, extraArgs)
}

// ─── sims_shutdown ──────────────────────────────────────────────────

func (a *App) toolSimsShutdown() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_shutdown",
		Description: "Shut down a sim. Idempotent — returns ok even when the sim is already shutdown or unknown.",
		InputSchema: schemaObject(map[string]any{
			"sim_id": map[string]any{
				"type":        "string",
				"description": "AVD name (android) or simctl UDID (ios).",
			},
		}, []string{"sim_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			if simID == "" {
				return nil, errors.New("sim_id required")
			}
			row, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if row == nil {
				// Unknown sim — idempotent success rather than an error.
				return map[string]any{"ok": true, "note": "sim not known to this project"}, nil
			}
			a.stopStream(simID)
			if p := a.sup.get(simID); p != nil {
				a.sup.shutdownProcess(p)
				a.sup.drop(simID)
			}
			// Belt-and-suspenders graceful paths: even if the
			// supervisor didn't have a tracked entry (sidecar
			// restarted, etc.), send the platform-native shutdown
			// signal so the device-manager service flushes state.
			switch row.Platform {
			case "android":
				if row.Serial != "" {
					_ = shutdownAndroidSim(row.Serial)
				}
			case "ios":
				_ = shutdownIOSSim(simID)
			}
			_ = dbUpdateSim(ctx.AppDB(), simID, map[string]any{
				"status": "shutdown", "pid": 0,
			})
			_ = dbDeleteStreamToken(ctx.AppDB(), simID)
			_ = dbStopActiveSimRuns(ctx.AppDB(), simID)
			return map[string]any{"ok": true}, nil
		},
	}
}

// ─── sims_screenshot ────────────────────────────────────────────────

func (a *App) toolSimsScreenshot() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_screenshot",
		Description: "Capture a one-off PNG screenshot of a booted sim. For continuous frames use sims_stream_url + the WebSocket.",
		InputSchema: schemaObject(map[string]any{
			"sim_id": map[string]any{
				"type":        "string",
				"description": "AVD name (android) or simctl UDID (ios).",
			},
		}, []string{"sim_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			if simID == "" {
				return nil, errors.New("sim_id required")
			}
			row, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, fmt.Errorf("sim %q not known to this project", simID)
			}
			if row.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted (status=%s)", simID, row.Status)
			}
			var png []byte
			switch row.Platform {
			case "android":
				png, err = androidScreenshot(row.Serial)
			case "ios":
				png, err = iosScreenshot(row.ID)
			default:
				return nil, fmt.Errorf("unknown platform %q for sim %q", row.Platform, simID)
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"png_b64": base64.StdEncoding.EncodeToString(png),
				"bytes":   len(png),
			}, nil
		},
	}
}

// splitArgs splits a config string like "-no-window -no-audio" into
// the slice the os/exec API expects. Doesn't support quoted args —
// keep config values whitespace-separated. (If a user genuinely needs
// quoting we'll grow this into a real shell-like parser; YAGNI now.)
func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := []string{}
	for _, tok := range strings.Fields(s) {
		out = append(out, tok)
	}
	return out
}
