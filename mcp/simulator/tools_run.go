package main

// sims_build / sims_install / sims_launch / sims_run tool bodies. Split
// from tools.go to keep the orchestration-heavy handlers together; the
// shared build/install/launch logic lives in run.go.

import (
	"context"
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
			"host_id":        map[string]any{"type": "integer", "description": "Optional Instances host override. 0 runs locally."},
		}, []string{"framework", "source_tgz_b64"}),
		HandlerCtx: a.handleSimsRun,
	}
}

func (a *App) handleSimsRun(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	framework := strArg(args, "framework")
	if framework != "android" && framework != "ios" {
		return nil, fmt.Errorf("framework must be android or ios, got %q", framework)
	}
	hostID := resolvedHostID(ctx, args, framework)
	if err := a.capabilityCheckForHost(ctx, framework, hostID, true, true); err != nil {
		return nil, err
	}
	proj, err := projectIDFor(ctx, args)
	if err != nil {
		return nil, err
	}

	// 1. Boot-if-needed.
	sim, err := a.ensureBootedSimOnHost(ctx, framework, hostID)
	if err != nil {
		return nil, fmt.Errorf("boot: %w", err)
	}
	unlockRun := a.lockSimRun(sim.ID)
	defer unlockRun()
	if err := dbStopActiveSimRuns(ctx.AppDB(), sim.ID); err != nil {
		return nil, err
	}

	// 2. Open a sim_runs row up front so the build log path is known
	//    and the panel can tail it while the build runs.
	sourceApp := strArg(args, "source_app")
	if sourceApp == "" {
		sourceApp = "manual"
	}
	run, err := dbInsertSimRun(ctx.AppDB(), SimRun{
		SimID:      sim.ID,
		ProjectID:  proj,
		SourceApp:  sourceApp,
		SourceRef:  strArg(args, "source_ref"),
		Framework:  framework,
		Status:     "building",
		RunnerKind: sim.RunnerKind,
		InstanceID: sim.InstanceID,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}

	// 3. Build.
	br, artifactID, err := a.buildForSim(callCtx, ctx, sim, buildParams{
		Framework:    framework,
		SourceTGZB64: strArg(args, "source_tgz_b64"),
		Module:       strArg(args, "android_module"),
		Scheme:       strArg(args, "ios_scheme"),
		BuildCmd:     strArg(args, "build_cmd"),
		SimUDID:      sim.NativeID(),
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
		"artifact_id":   artifactID,
		"log_path":      fmt.Sprintf("%d.log", run.ID),
	})

	// 4. Install + launch.
	if err := a.installAndLaunchForSim(ctx, sim, br, artifactID); err != nil {
		a.failRun(ctx, run.ID, "install/launch: "+err.Error())
		return nil, err
	}
	_ = dbUpdateSimRun(ctx.AppDB(), run.ID, map[string]any{"status": "running"})
	_ = a.cleanupStorage(ctx.AppDB())

	// 5. Mint the stream token.
	stream, err := dbMintStreamToken(ctx.AppDB(), sim.ID, 1*time.Hour)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"sim_id":      sim.ID,
		"sim_run_id":  run.ID,
		"platform":    sim.Platform,
		"bundle_id":   br.BundleID,
		"artifact_id": artifactID,
		"runner":      sim.RunnerKind,
		"instance_id": sim.InstanceID,
		"stream_url":  a.streamURL(ctx, sim.ID, stream.WSToken),
		"status":      "running",
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
			"host_id":        map[string]any{"type": "integer", "description": "Optional Instances host override. 0 runs locally."},
		}, []string{"framework", "source_tgz_b64"}),
		HandlerCtx: func(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			framework := strArg(args, "framework")
			hostID := resolvedHostID(ctx, args, framework)
			if err := a.capabilityCheckForHost(ctx, framework, hostID, true, false); err != nil {
				return nil, err
			}
			// Resolve a destination sim. A specific sim_id wins; else
			// reuse/boot one. Both platforms key the run to a real
			// sims row so the FK holds and iOS gets its build
			// destination.
			var sim *Sim
			if id := strArg(args, "sim_id"); id != "" {
				s, err := dbGetProjectSim(ctx, args, id)
				if err != nil {
					return nil, err
				}
				if s == nil || s.Status != "booted" || s.InstanceID != hostID {
					return nil, fmt.Errorf("sim %q not booted", id)
				}
				sim = s
			} else {
				s, err := a.ensureBootedSimOnHost(ctx, framework, hostID)
				if err != nil {
					return nil, err
				}
				sim = s
			}
			unlockRun := a.lockSimRun(sim.ID)
			defer unlockRun()
			if err := dbStopActiveSimRuns(ctx.AppDB(), sim.ID); err != nil {
				return nil, err
			}
			run, err := dbInsertSimRun(ctx.AppDB(), SimRun{
				SimID: sim.ID, ProjectID: ctx.CurrentProject(),
				SourceApp: "manual", Framework: framework, Status: "building",
				StartedAt:  time.Now().UTC().Format(time.RFC3339),
				RunnerKind: sim.RunnerKind, InstanceID: sim.InstanceID,
			})
			if err != nil {
				return nil, err
			}
			br, artifactID, err := a.buildForSim(callCtx, ctx, sim, buildParams{
				Framework: framework, SourceTGZB64: strArg(args, "source_tgz_b64"),
				Module: strArg(args, "android_module"), Scheme: strArg(args, "ios_scheme"),
				BuildCmd: strArg(args, "build_cmd"), SimUDID: sim.NativeID(), SimRunID: run.ID,
			})
			if err != nil {
				a.failRun(ctx, run.ID, err.Error())
				return nil, err
			}
			_ = dbUpdateSimRun(ctx.AppDB(), run.ID, map[string]any{
				"status": "stopped", "bundle_id": br.BundleID, "artifact_path": br.ArtifactPath,
				"artifact_id": artifactID,
				"log_path":    fmt.Sprintf("%d.log", run.ID), "stopped_at": time.Now().UTC().Format(time.RFC3339),
			})
			_ = a.cleanupStorage(ctx.AppDB())
			return map[string]any{
				"sim_id":        sim.ID,
				"sim_run_id":    run.ID,
				"bundle_id":     br.BundleID,
				"artifact_path": br.ArtifactPath,
				"artifact_id":   artifactID,
				"runner":        sim.RunnerKind,
				"instance_id":   sim.InstanceID,
			}, nil
		},
	}
}

// ─── sims_install ───────────────────────────────────────────────────

func (a *App) toolSimsInstall() sdk.Tool {
	return sdk.Tool{
		Name:        "sims_install",
		Description: "Install a previously-built artifact onto a booted sim. Local sims accept artifact_path; remote sims require artifact_id.",
		InputSchema: schemaObject(map[string]any{
			"sim_id":        map[string]any{"type": "string", "description": "Required. Target sim."},
			"artifact_path": map[string]any{"type": "string", "description": "Required. Path from sims_build."},
			"artifact_id":   map[string]any{"type": "string", "description": "Opaque artifact id from sims_build; required for remote sims."},
		}, []string{"sim_id"}),
		Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			simID := strArg(args, "sim_id")
			artifact := strArg(args, "artifact_path")
			artifactID := strArg(args, "artifact_id")
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
			if err := a.installArtifactForSim(ctx, sim, artifact, artifactID); err != nil {
				return nil, err
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
			sim, err := dbGetProjectSim(ctx, args, simID)
			if err != nil {
				return nil, err
			}
			if sim == nil || sim.Status != "booted" {
				return nil, fmt.Errorf("sim %q not booted", simID)
			}
			if err := a.launchBundleForSim(ctx, sim, bundleID); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}
