package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Per-tenant apteva version management.
//
// Each tenant runs an apteva child process; we pick which binary to
// spawn by checking Tenant.TargetVersion. When set, the versioned
// binary at <fleetRoot>/versions/<v>/node_modules/.bin/apteva is used;
// when empty, fall back to the host-wide `apteva` (resolveAptevaBin).
//
// `tenant_update` installs the requested version into a fleet-owned
// npm prefix (NOT the global one — we don't touch host `npm i -g
// apteva`), records target_version, and respawns the tenant. Other
// tenants are unaffected.
//
// Scope of v0.3.0: manual update only. Auto-update (4c in the
// proposal) — comparing latest vs. policy — is deferred; the
// information needed to surface "update available" already lives in
// (current_version, target_version, npm latest) and can be added to
// the panel without a new background worker.

// versionInstallMu serialises npm install for the same fleet process.
// Multiple tenant_update calls for the same version race for cache
// dir creation; rather than installing twice, hold a lock for the
// whole resolve-and-install path.
var versionInstallMu sync.Mutex

// httpUpdateClient is separate from httpClient (the health poller's
// short-timeout client) — npm metadata calls are typically fast but
// shouldn't share the tight 10s budget.
var httpUpdateClient = &http.Client{Timeout: 30 * time.Second}

// versionsRoot is where each installed version lives. Override with
// FLEET_VERSIONS_ROOT — primarily for tests.
func versionsRoot() string {
	if v := os.Getenv("FLEET_VERSIONS_ROOT"); v != "" {
		return v
	}
	return filepath.Join(localDataRoot(), "versions")
}

// tenantAptevaBin returns the path of the apteva binary fleet should
// use for a tenant. Empty target_version → empty string → caller
// falls back to resolveAptevaBin's default. The path is NOT validated
// here (we don't want spawn-time IO inside store reads); ensure-install
// is the one source of truth for "is this version usable".
func tenantAptevaBin(targetVersion string) string {
	v := strings.TrimSpace(targetVersion)
	if v == "" {
		return ""
	}
	return filepath.Join(versionsRoot(), v, "node_modules", ".bin", "apteva")
}

// ─── npm registry lookup ──────────────────────────────────────────

// npmLatestVersion asks the public npm registry for `apteva@latest`.
// The /latest endpoint always returns the maintainer-published latest
// dist-tag — exactly what `npm i apteva@latest` would resolve to.
// Override the registry via NPM_REGISTRY for air-gapped / mirrored
// installs.
func npmLatestVersion(ctx context.Context) (string, error) {
	reg := strings.TrimSuffix(strings.TrimSpace(os.Getenv("NPM_REGISTRY")), "/")
	if reg == "" {
		reg = "https://registry.npmjs.org"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg+"/apteva/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := httpUpdateClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry %s: status %d", reg, resp.StatusCode)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Version == "" {
		return "", errors.New("npm registry returned no version field")
	}
	return body.Version, nil
}

// ─── per-version install ──────────────────────────────────────────

// ensureVersionInstalled is idempotent: if the version is already in
// the cache and the binary is executable, return its path; otherwise
// run `npm install --prefix <dir> apteva@<version>` and return.
//
// This uses npm's local-install model (target dir gets a node_modules/
// and .bin/), so the host's global node_modules is untouched. Each
// version is fully isolated.
func ensureVersionInstalled(ctx context.Context, version string) (binPath string, err error) {
	versionInstallMu.Lock()
	defer versionInstallMu.Unlock()

	dir := filepath.Join(versionsRoot(), version)
	bin := filepath.Join(dir, "node_modules", ".bin", "apteva")
	if fi, statErr := os.Stat(bin); statErr == nil && fi.Mode()&0o111 != 0 {
		return bin, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir versions dir: %w", err)
	}

	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", errors.New("npm not on PATH — needed to install per-tenant apteva versions")
	}
	cmd := exec.CommandContext(ctx, npm, "install",
		"--prefix", dir,
		"--no-audit", "--no-fund", "--no-save", "--silent",
		"apteva@"+version,
	)
	// Capture combined output so a failed install surfaces in the
	// returned error — npm's exit-code-only failure mode is useless
	// for operators.
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", fmt.Errorf("npm install apteva@%s: %v: %s", version, runErr, truncate(string(out), 600))
	}
	if fi, statErr := os.Stat(bin); statErr != nil || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("npm install completed but %s is missing/non-exec", bin)
	}
	return bin, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}

// ─── tool handlers ────────────────────────────────────────────────

// toolUpdate: manual per-tenant version update.
//
//	{tenant_id, version?}  // version empty → npm `apteva@latest`
//
// Behaviour:
//   - remote tenants are read-only; this returns an error
//   - if version already matches current_version, no-op + return
//   - install the version into the fleet-owned cache
//   - persist target_version
//   - stop the running process, respawn with the versioned binary
//   - return updated tenant + new current_version once health probe
//     observes it
func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if t.Kind != KindLocal {
		return nil, errors.New("tenant_update only works on local tenants; for remote, upgrade out-of-band")
	}
	requested := strings.TrimSpace(getStr(args, "version"))
	if requested == "" {
		v, lookupErr := npmLatestVersion(context.Background())
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve latest version: %w", lookupErr)
		}
		requested = v
	}
	if requested == t.CurrentVersion && requested == t.TargetVersion {
		return map[string]any{
			"tenant":  a.publicTenantView(t),
			"updated": false,
			"reason":  "already at requested version",
		}, nil
	}

	bin, err := ensureVersionInstalled(context.Background(), requested)
	if err != nil {
		_ = a.store.recordEvent(t.ID, "update_failed", "tool:tenant_update",
			map[string]any{"version": requested, "stage": "install", "error": err.Error()})
		return nil, err
	}

	// Persist target_version BEFORE the spawn so an auto-respawn after
	// a crash mid-update still picks the new binary.
	if err := a.store.setTargetVersion(t.ID, requested); err != nil {
		return nil, err
	}

	// Stop, respawn. The existing handle (procs[slug]) is from the
	// old binary; stopProcess SIGTERMs then SIGKILLs. Then spawn with
	// the new binary path.
	a.procMu.Lock()
	prev := a.procs[t.Slug]
	a.procMu.Unlock()
	if prev != nil {
		_ = stopProcess(prev, 10*time.Second)
	}
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 || t.ConfigDir == "" {
		return nil, errors.New("tenant missing port or config_dir — cannot respawn")
	}
	_, proc, spawnErr := a.spawnTenant(context.Background(), t.Slug, t.ConfigDir, bin, port, false)
	if spawnErr != nil {
		_ = a.store.recordEvent(t.ID, "update_failed", "tool:tenant_update",
			map[string]any{"version": requested, "stage": "spawn", "error": spawnErr.Error()})
		return nil, fmt.Errorf("respawn with apteva@%s: %w", requested, spawnErr)
	}
	a.procMu.Lock()
	a.procs[t.Slug] = proc
	a.procMu.Unlock()
	_ = a.store.recordEvent(t.ID, "updated", "tool:tenant_update",
		map[string]any{"version": requested, "bin": bin})

	updated, _, _ := a.store.get(t.ID)
	return map[string]any{
		"tenant":  a.publicTenantView(updated),
		"updated": true,
		"version": requested,
		"note":    "process respawned; current_version will reflect once the next health poll runs",
	}, nil
}

// toolCheckUpdates returns the npm latest version + per-tenant drift
// without applying anything. Read-only.
func (a *App) toolCheckUpdates(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	latest, err := npmLatestVersion(context.Background())
	if err != nil {
		return nil, err
	}
	tenants, err := a.store.list(map[string]string{"kind": KindLocal})
	if err != nil {
		return nil, err
	}
	drift := []map[string]any{}
	for _, t := range tenants {
		if t.CurrentVersion == "" || t.CurrentVersion == latest {
			continue
		}
		drift = append(drift, map[string]any{
			"tenant_id":       t.ID,
			"slug":            t.Slug,
			"current_version": t.CurrentVersion,
			"target_version":  t.TargetVersion,
		})
	}
	return map[string]any{
		"latest":          latest,
		"tenants_behind":  drift,
		"checked_at":      time.Now().UTC(),
	}, nil
}
