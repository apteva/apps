package main

// The build+install+launch orchestration shared by sims_run (composite)
// and the individual sims_build / sims_install / sims_launch tools.
// Keeping the orchestration here (rather than inline in tools.go) lets
// the standalone panel's manual flow and Code's sims_run path share one
// implementation.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// buildParams carries everything a build needs, platform-agnostic at
// the call site; the dispatcher branches on Framework.
type buildParams struct {
	Framework     string // "android" | "ios"
	SourceTGZB64  string
	Module        string // android module (":app:" default when empty)
	Scheme        string // ios scheme (first scheme when empty)
	BuildCmd      string // shell override; wins over module/scheme
	SimUDID       string // ios needs a booted sim's udid as the build destination
	SimRunID      int64  // for log-file naming
}

// buildResult is the platform-agnostic result the orchestration layer
// returns up to the tools.
type buildResult struct {
	ArtifactPath string
	BundleID     string
	Activity     string // android launchable activity (empty for ios)
}

// runBuild extracts the source archive and dispatches to the platform
// builder. Writes the combined build output to the sim_run's log file.
func (a *App) runBuild(ctx *sdk.AppCtx, p buildParams) (*buildResult, error) {
	if strings.TrimSpace(p.SourceTGZB64) == "" {
		return nil, fmt.Errorf("source_tgz_b64 required")
	}
	srcDir := filepath.Join(os.TempDir(), "apteva-sim-src-"+randHex(8))
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(srcDir)
	if _, err := extractSourceTarGz(p.SourceTGZB64, srcDir); err != nil {
		return nil, err
	}

	logPath := filepath.Join(a.simLogsDir, fmt.Sprintf("%d.log", p.SimRunID))
	logW, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open build log: %w", err)
	}
	defer logW.Close()
	fmt.Fprintf(logW, "=== build %s at %s ===\n", p.Framework, time.Now().UTC().Format(time.RFC3339))

	switch p.Framework {
	case "android":
		res, err := a.buildAndroid(srcDir, p.Module, p.BuildCmd, splitArgs(ctx.Config().Get("gradle_extra_args")), logW)
		if err != nil {
			return nil, err
		}
		activity, _ := androidLaunchableActivity(res.APKPath) // best-effort
		return &buildResult{ArtifactPath: res.APKPath, BundleID: res.BundleID, Activity: activity}, nil
	case "ios":
		if p.SimUDID == "" {
			return nil, fmt.Errorf("ios build requires a booted sim udid as destination")
		}
		res, err := a.buildIOS(srcDir, p.Scheme, p.BuildCmd, p.SimUDID, splitArgs(ctx.Config().Get("xcodebuild_extra_args")), logW)
		if err != nil {
			return nil, err
		}
		return &buildResult{ArtifactPath: res.AppPath, BundleID: res.BundleID}, nil
	}
	return nil, fmt.Errorf("unknown framework %q", p.Framework)
}

// installAndLaunch installs a built artifact onto the sim and launches
// it. Platform dispatch on the sim row.
func (a *App) installAndLaunch(sim *Sim, br *buildResult) error {
	switch sim.Platform {
	case "android":
		if err := installAndroidAPK(sim.Serial, br.ArtifactPath); err != nil {
			return err
		}
		return launchAndroid(sim.Serial, br.BundleID, br.Activity)
	case "ios":
		if err := installIOSApp(sim.ID, br.ArtifactPath); err != nil {
			return err
		}
		return launchIOS(sim.ID, br.BundleID)
	}
	return fmt.Errorf("unknown platform %q", sim.Platform)
}

// ensureBootedSim returns a booted sim for the framework, booting one
// if none is live. Reuses the most-recent booted sim for the platform
// in this project — sims_run is "give me my app running", not "give me
// a fresh device every time".
func (a *App) ensureBootedSim(ctx *sdk.AppCtx, framework string) (*Sim, error) {
	proj := ctx.CurrentProject()
	sims, err := dbListSims(ctx.AppDB(), proj)
	if err != nil {
		return nil, err
	}
	for _, s := range sims {
		if s.Platform == framework && s.Status == "booted" {
			// Confirm it's actually alive — DB can be stale after a
			// host-side shutdown the sidecar didn't observe.
			if a.sup.probeAlive(s) {
				row := s
				return &row, nil
			}
		}
	}
	// Nothing live — boot a fresh one with config defaults.
	switch framework {
	case "android":
		return a.bootAndroid(ctx, ctx.Config().Get("android_image"), ctx.Config().Get("android_device_type"))
	case "ios":
		return a.bootIOS(ctx, ctx.Config().Get("ios_runtime"), ctx.Config().Get("ios_device_type"))
	}
	return nil, fmt.Errorf("unknown framework %q", framework)
}

// streamURL builds the absolute WebSocket URL for a sim's live stream.
// Prefers the platform's configured public URL; falls back to a
// relative path the panel resolves against its own origin.
func (a *App) streamURL(ctx *sdk.AppCtx, simID, token string) string {
	rel := fmt.Sprintf("/api/apps/simulator/stream/%s?t=%s", simID, token)
	info, err := ctx.PlatformInfo()
	if err != nil || info == nil || info.PublicURL == "" {
		return rel
	}
	base := strings.TrimRight(info.PublicURL, "/")
	wsBase := base
	switch {
	case strings.HasPrefix(base, "https://"):
		wsBase = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		wsBase = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return wsBase + rel
}

// runContext bounds the whole sims_run orchestration so a hung gradle /
// xcodebuild can't wedge the handler forever. 20 minutes is generous
// for a cold first build (gradle dependency download, Xcode index).
func runContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 20*time.Minute)
}
