package main

// sims_build / sims_install / sims_launch / sims_run tool bodies. Split
// from tools.go to keep the orchestration-heavy handlers together; the
// shared build/install/launch logic lives in run.go.

import (
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── sims_run (composite — the entry point Code calls) ──────────────

func (a *App) toolSimsRun() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_run",
		Description: "Boot a sim if needed, build the source archive, install, launch, and return a live stream URL. The one-call entry point the Code app uses for an iOS/Android repo.",
		InputSchema: schemaObject(map[string]any{
			"framework":      map[string]any{"type": "string", "enum": []string{"android", "ios"}, "description": "Required."},
			"source_tgz_b64": map[string]any{"type": "string", "description": "Required. base64(gzip(tar)) of the repo source."},
			"source_app":     map[string]any{"type": "string", "description": "Caller tag — 'code' | 'manual'. Default 'manual'."},
			"source_ref":     map[string]any{"type": "string", "description": "Caller correlation id (repo_id when source_app=code)."},
			"ios_scheme":     map[string]any{"type": "string", "description": "iOS only. Empty = first scheme from xcodebuild -list."},
			"android_module": map[string]any{"type": "string", "description": "Android only. Gradle module producing the APK. Empty = app."},
			"build_cmd":      map[string]any{"type": "string", "description": "Shell override for the build step. Wins over scheme/module."},
		}, []string{"framework", "source_tgz_b64"}),
		Handler: a.handleSimsRun,
	}
}

func (a *App) handleSimsRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	framework := strArg(args, "framework")
	if framework != "android" && framework != "ios" {
		return nil, fmt.Errorf("framework must be android or ios, got %q", framework)
	}
	if err := capabilityCheckFor(ctx, framework); err != nil {
		return nil, err // host_unsupported: ...
	}
	proj, err := projectIDFor(ctx, args)
	if err != nil {
		return nil, err
	}

	// 1. Boot-if-needed.
	sim, err := a.ensureBootedSim(ctx, framework)
	if err != nil {
		return nil, fmt.Errorf("boot: %w", err)
	}

	// 2. Open a sim_runs row up front so the build log path is known
	//    and the panel can tail it while the build runs.
	sourceApp := strArg(args, "source_app")
	if sourceApp == "" {
		sourceApp = "manual"
	}
	run, err := dbInsertSimRun(ctx.AppDB(), SimRun{
		SimID:     sim.ID,
		ProjectID: proj,
		SourceApp: sourceApp,
		SourceRef: strArg(args, "source_ref"),
		Framework: framework,
		Status:    "building",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}

	// 3. Build.
	br, err := a.runBuild(ctx, buildParams{
		Framework:    framework,
		SourceTGZB64: strArg(args, "source_tgz_b64"),
		Module:       strArg(args, "android_module"),
		Scheme:       strArg(args, "ios_scheme"),
		BuildCmd:     strArg(args, "build_cmd"),
		SimUDID:      sim.ID,
		SimRunID:     run.ID,
	})
	if err != nil {
		a.failRun(ctx, run.ID, "build: "+err.Error())
		return nil, err
	}
	_ = dbUpdateSimRun(ctx.AppDB(), run.ID, map[string]any{
		"status":        "installing",
		"bundle_id":     br.BundleID,
		"artifact_path": br.ArtifactPath,
		"log_path":      fmt.Sprintf("%d.log", run.ID),
	})

	// 4. Install + launch.
	if err := a.installAndLaunch(sim, br); err != nil {
		a.failRun(ctx, run.ID, "install/launch: "+err.Error())
		return nil, err
	}
	_ = dbUpdateSimRun(ctx.AppDB(), run.ID, map[string]any{"status": "running"})

	// 5. Mint the stream token.
	stream, err := dbMintStreamToken(ctx.AppDB(), sim.ID, 1*time.Hour)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"sim_id":     sim.ID,
		"sim_run_id": run.ID,
		"platform":   sim.Platform,
		"bundle_id":  br.BundleID,
		"stream_url": a.streamURL(ctx, sim.ID, stream.WSToken),
		"status":     "running",
	}, nil
}

func (a *App) failRun(ctx *sdk.AppCtx, runID int64, msg string) {
	_ = dbUpdateSimRun(ctx.AppDB(), runID, map[string]any{
		"status":     "crashed",
		"stopped_at": time.Now().UTC().Format(time.RFC3339),
		"error":      msg,
	})
}

// ─── sims_build ─────────────────────────────────────────────────────

func (a *App) toolSimsBuild() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_build",
		Description: "Build a repo source archive into an APK (android) or .app (ios) without launching it. A sim is booted if none is live — iOS needs one as the build destination, and keying the run to a real sim keeps the artifact reproducible per device arch.",
		InputSchema: schemaObject(map[string]any{
			"framework":      map[string]any{"type": "string", "enum": []string{"android", "ios"}, "description": "Required."},
			"source_tgz_b64": map[string]any{"type": "string", "description": "Required. base64(gzip(tar)) of the repo source."},
			"sim_id":         map[string]any{"type": "string", "description": "Target a specific booted sim. Empty = reuse/boot one."},
			"ios_scheme":     map[string]any{"type": "string", "description": "iOS only."},
			"android_module": map[string]any{"type": "string", "description": "Android only."},
			"build_cmd":      map[string]any{"type": "string", "description": "Shell override."},
		}, []string{"framework", "source_tgz_b64"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			framework := strArg(args, "framework")
			if err := capabilityCheckFor(ctx, framework); err != nil {
				return nil, err
			}
			// Resolve a destination sim. A specific sim_id wins; else
			// reuse/boot one. Both platforms key the run to a real
			// sims row so the FK holds and iOS gets its build
			// destination.
			var sim *Sim
			if id := strArg(args, "sim_id"); id != "" {
				s, err := dbGetSim(ctx.AppDB(), id)
				if err != nil {
					return nil, err
				}
				if s == nil || s.Status != "booted" {
					return nil, fmt.Errorf("sim %q not booted", id)
				}
				sim = s
			} else {
				s, err := a.ensureBootedSim(ctx, framework)
				if err != nil {
					return nil, err
				}
				sim = s
			}
			run, err := dbInsertSimRun(ctx.AppDB(), SimRun{
				SimID: sim.ID, ProjectID: ctx.CurrentProject(),
				SourceApp: "manual", Framework: framework, Status: "building",
				StartedAt: time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				return nil, err
			}
			br, err := a.runBuild(ctx, buildParams{
				Framework: framework, SourceTGZB64: strArg(args, "source_tgz_b64"),
				Module: strArg(args, "android_module"), Scheme: strArg(args, "ios_scheme"),
				BuildCmd: strArg(args, "build_cmd"), SimUDID: sim.ID, SimRunID: run.ID,
			})
			if err != nil {
				a.failRun(ctx, run.ID, err.Error())
				return nil, err
			}
			_ = dbUpdateSimRun(ctx.AppDB(), run.ID, map[string]any{
				"status": "stopped", "bundle_id": br.BundleID, "artifact_path": br.ArtifactPath,
				"log_path": fmt.Sprintf("%d.log", run.ID),
			})
			return map[string]any{
				"sim_id":        sim.ID,
				"sim_run_id":    run.ID,
				"bundle_id":     br.BundleID,
				"artifact_path": br.ArtifactPath,
			}, nil
		},
	}
}

// ─── sims_install ───────────────────────────────────────────────────

func (a *App) toolSimsInstall() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_install",
		Description: "Install a previously-built artifact onto a booted sim. Provide the artifact_path from a sims_build result.",
		InputSchema: schemaObject(map[string]any{
			"sim_id":        map[string]any{"type": "string", "description": "Required. Target sim."},
			"artifact_path": map[string]any{"type": "string", "description": "Required. Path from sims_build."},
		}, []string{"sim_id", "artifact_path"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			artifact := strArg(args, "artifact_path")
			if simID == "" || artifact == "" {
				return nil, errors.New("sim_id and artifact_path required")
			}
			sim, err := dbGetSim(ctx.AppDB(), simID)
			if err != nil {
				return nil, err
			}
			if sim == nil || sim.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted", simID)
			}
			switch sim.Platform {
			case "android":
				if err := installAndroidAPK(sim.Serial, artifact); err != nil {
					return nil, err
				}
			case "ios":
				if err := installIOSApp(sim.ID, artifact); err != nil {
					return nil, err
				}
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

// ─── sims_launch ────────────────────────────────────────────────────

func (a *App) toolSimsLaunch() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_launch",
		Description: "Launch an installed bundle on a booted sim by bundle/package id.",
		InputSchema: schemaObject(map[string]any{
			"sim_id":    map[string]any{"type": "string", "description": "Required. Target sim."},
			"bundle_id": map[string]any{"type": "string", "description": "Required. Package name (android) or bundle id (ios)."},
		}, []string{"sim_id", "bundle_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			bundleID := strArg(args, "bundle_id")
			if simID == "" || bundleID == "" {
				return nil, errors.New("sim_id and bundle_id required")
			}
			sim, err := dbGetSim(ctx.AppDB(), simID)
			if err != nil {
				return nil, err
			}
			if sim == nil || sim.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted", simID)
			}
			switch sim.Platform {
			case "android":
				if err := launchAndroid(sim.Serial, bundleID, ""); err != nil {
					return nil, err
				}
			case "ios":
				if err := launchIOS(sim.ID, bundleID); err != nil {
					return nil, err
				}
			}
			return map[string]any{"ok": true}, nil
		},
	}
}
