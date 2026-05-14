package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// buildTimeout bounds a dependency install. Generous — a cold
// `bun install` with a real dep tree can take a while — but finite.
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

// ensureBuilt makes sure version v's artifact dir exists and is
// populated: the entry file written, and `npm`/`bun install` run if
// the version ships a package.json. Idempotent — a `.ready` marker
// lets it no-op on a dir that's already built, so it's safe to call
// from both deploy (first build) and the pool's cold-start path
// (rebuild after a restart cleared an ephemeral build base).
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
	if strings.TrimSpace(v.PackageJSON) != "" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(v.PackageJSON), 0o600); err != nil {
			return "", err
		}
		if err := runInstall(dir, spec); err != nil {
			return "", err
		}
	}
	// Marker written last — its presence means "fully built".
	if err := os.WriteFile(marker, []byte(v.SourceHash), 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// runInstall runs the runtime's dependency installer in dir.
func runInstall(dir string, spec runtimeSpec) error {
	var bin string
	var args []string
	switch spec.Name {
	case "node":
		bin, args = "npm", []string{"install", "--no-audit", "--no-fund", "--loglevel", "error"}
	default: // bun
		bin, args = "bun", []string{"install"}
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("dependency install needs %q on PATH", bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Dir = dir
	out := newCapBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s install failed: %v\n%s", bin, err, out.String())
	}
	return nil
}
