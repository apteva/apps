package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// buildTimeout bounds a build step. Generous — a cold `npm install`
// or a `go build` with a populated module cache can take a while —
// but finite.
const buildTimeout = 2 * time.Minute

// poolBuildBase returns the root under which version artifact dirs
// live. It lives on the pool (created per sidecar boot) so artifact
// paths stay stable for the process lifetime — and so each test's
// fresh AppCtx gets its own base instead of colliding on fn-1/v1.
func poolBuildBase() (string, error) {
	if globalPool == nil {
		return "", errors.New("function worker pool not initialised")
	}
	return globalPool.buildBase, nil
}

// versionDir is the deterministic artifact path for one version.
func versionDir(base string, v *FunctionVersion) string {
	return filepath.Join(base, fmt.Sprintf("fn-%d", v.FunctionID), fmt.Sprintf("v%d", v.Version))
}

// ensureBuilt makes sure version v's artifact dir exists and is fully
// built: the entry file written, the runtime's support files staged
// (Stage), and the runtime's build step run (Build — `npm install`
// for node deps, `go build` for go). Idempotent — a `.ready` marker
// lets it no-op on a dir that's already built, so it's safe to call
// from both deploy and the pool's cold-start path (e.g. a rebuild
// after a restart cleared an ephemeral build base).
func ensureBuilt(base string, v *FunctionVersion, spec runtimeSpec, src []byte) (string, error) {
	dir := versionDir(base, v)
	marker := filepath.Join(dir, ".ready")
	if _, err := os.Stat(marker); err == nil {
		return dir, nil // already built
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, spec.EntryFile), src, 0o600); err != nil {
		return "", err
	}
	if err := spec.Stage(dir); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}
	if err := spec.Build(dir, v.PackageJSON); err != nil {
		return "", err
	}
	// Marker written last — its presence means "fully built".
	if err := os.WriteFile(marker, []byte(v.SourceHash), 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// runBuildCmd runs a runtime build step in dir, capturing combined
// output for the error message. Bounded by buildTimeout.
func runBuildCmd(dir, label, bin string, args ...string) error {
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s needs %q on PATH", label, bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Dir = dir
	out := newCapBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %v\n%s", label, err, out.String())
	}
	return nil
}

func runNpmInstall(dir string) error {
	return runBuildCmd(dir, "npm install", "npm",
		"install", "--no-audit", "--no-fund", "--loglevel", "error")
}

func runGoBuild(dir string) error {
	return runBuildCmd(dir, "go build", "go", "build", "-o", "worker", ".")
}
