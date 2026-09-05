package main

// Remote dev runs — mobile repos delegated to the Simulator app.
//
// Web frameworks spawn a local child process (dev_runtime.go). Mobile
// frameworks (ios / android) can't: they need an emulator/simulator,
// a build toolchain, and screen streaming. Instead the Code sidecar
// packages the repo source as a gzip tarball and calls the Simulator
// app's sims_run over the SDK's cross-app RPC. Simulator boots a sim,
// builds, installs, launches, and returns a live-stream URL the panel
// embeds.
//
// The dev_runs row records runner="simulator" plus the sim handle and
// stream URL so repos_dev_status / repos_dev_stop route correctly and
// the panel knows to render a device frame instead of an iframe.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// simulatorAppName is the manifest name of the Simulator app this
// integration binds to. Matches requires.integrations[].compatible_app_names.
const simulatorAppName = "simulator"

// remoteSrcExclusions are directory/file segments stripped from the
// source tarball before it's shipped to the Simulator app. Build
// caches and VCS metadata are large and never needed for a fresh
// build; shipping them would blow past sane payload sizes. Superset of
// shouldSkipForExport plus the mobile-specific caches.
var remoteSrcExclusions = map[string]bool{
	"node_modules": true,
	".git":         true,
	".gradle":      true,
	"build":        true,
	".next":        true,
	"dist":         true,
	".cache":       true,
	"Pods":         true,
	"DerivedData":  true,
}

var remoteSrcExtExclusions = map[string]bool{
	".apk": true,
	".ipa": true,
	".aab": true,
}

// startRemoteRun tars the repo source, calls the Simulator app's
// sims_run, and persists a dev_runs row capturing the delegation.
func (s *devSupervisor) startRemoteRun(ctx *sdk.AppCtx, in startDevInput, srcDir, framework, runner string) (*DevRun, error) {
	if runner != simulatorAppName {
		return nil, fmt.Errorf("unknown remote runner %q", runner)
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable; cannot reach the Simulator app")
	}
	// Optional-dep guard: surface a clean, actionable error when the
	// Simulator app isn't bound rather than a confusing RPC failure.
	if bound := ctx.IntegrationFor("simulator"); bound == nil {
		return nil, errors.New("the Simulator app isn't installed/bound — install it to run iOS/Android repos")
	}

	tgz, err := tarRepoSource(srcDir)
	if err != nil {
		return nil, fmt.Errorf("package source: %w", err)
	}

	// Pull mobile build hints from the repo's deploy hints. build_cmd
	// is the generic shell override; the Simulator app interprets the
	// ios_scheme / android_module hints we thread from env_json's
	// reserved keys (kept simple in v1 — see repos_set_deploy_hints).
	input := map[string]any{
		"framework":      framework,
		"source_tgz_b64": tgz,
		"source_app":     "code",
		"source_ref":     fmt.Sprintf("%d", in.Repo.ID),
		"build_cmd":      in.Repo.BuildCmd,
		"_project_id":    in.ProjectID,
	}
	if scheme := mobileHint(in.Repo.EnvJSON, "ios_scheme"); scheme != "" {
		input["ios_scheme"] = scheme
	}
	if module := mobileHint(in.Repo.EnvJSON, "android_module"); module != "" {
		input["android_module"] = module
	}

	var out struct {
		SimID     string `json:"sim_id"`
		SimRunID  int64  `json:"sim_run_id"`
		Platform  string `json:"platform"`
		BundleID  string `json:"bundle_id"`
		StreamURL string `json:"stream_url"`
		Status    string `json:"status"`
	}
	if err := ctx.PlatformAPI().CallAppResult(simulatorAppName, "sims_run", input, &out); err != nil {
		// Persist a crashed row so the panel surfaces the failure.
		_, _ = dbUpsertDevRun(ctx.AppDB(), DevRun{
			ProjectID: in.ProjectID, RepoID: in.Repo.ID, Framework: framework,
			Runner: runner, Status: "crashed", Error: err.Error(),
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil, err
	}

	return dbUpsertDevRun(ctx.AppDB(), DevRun{
		ProjectID: in.ProjectID,
		RepoID:    in.Repo.ID,
		Framework: framework,
		Runner:    runner,
		Status:    "live",
		SimID:     out.SimID,
		StreamURL: out.StreamURL,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// stopRemoteRun preserves the recovery handle if Simulator shutdown fails.
func (s *devSupervisor) stopRemoteRun(ctx *sdk.AppCtx, dr *DevRun) error {
	if dr.SimID == "" {
		return nil
	}
	if ctx.PlatformAPI() == nil {
		return fmt.Errorf("platform unavailable; simulator was not stopped")
	}
	var result map[string]any
	return ctx.PlatformAPI().CallAppResult(simulatorAppName, "sims_shutdown", map[string]any{"sim_id": dr.SimID, "_project_id": dr.ProjectID}, &result)
}

// tarRepoSource walks srcDir and produces a base64(gzip(tar)) of the
// source tree, skipping build caches + VCS metadata. The tar paths are
// repo-relative so the Simulator app extracts a clean tree.
func tarRepoSource(srcDir string) (string, error) {
	var buf strings.Builder
	b64 := base64.NewEncoder(base64.StdEncoding, &buf)
	gz := gzip.NewWriter(b64)
	tw := tar.NewWriter(gz)

	limits := currentImportLimits()
	var total int64
	count := 0
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skipRemoteSrc(rel, info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil // tar gets dirs implicitly via file paths
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks/devices
		}
		count++
		if err := checkImportEntry(limits, rel, info.Size(), total, count); err != nil {
			return err
		}
		total += info.Size()
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := b64.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func skipRemoteSrc(rel string, info os.FileInfo) bool {
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		if remoteSrcExclusions[seg] {
			return true
		}
	}
	if !info.IsDir() {
		if remoteSrcExtExclusions[strings.ToLower(filepath.Ext(rel))] {
			return true
		}
	}
	return false
}

// mobileHint extracts a named hint from the repo's env_json blob. The
// repo's env_json doubles as a small key/value bag for mobile build
// hints (ios_scheme, android_module) in v1 — a dedicated column pair
// can come later if this proves load-bearing. Returns "" on any parse
// miss; callers treat empty as "use the Simulator app's default".
func mobileHint(envJSON, key string) string {
	envJSON = strings.TrimSpace(envJSON)
	if envJSON == "" || envJSON == "{}" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(envJSON), &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
