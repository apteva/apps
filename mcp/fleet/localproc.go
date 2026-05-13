package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// tenantProc tracks the live OS process of a local tenant.
type tenantProc struct {
	cmd     *exec.Cmd
	port    int
	started time.Time
}

// spawnTenant boots a new local apteva child:
//
//	<apteva-bin> --data-dir <configDir> --port <port> --no-browser
//
// In dashboard / --no-browser mode the apteva CLI deliberately does
// NOT auto-register an admin user — registration happens in the
// browser using a setup token that apteva-server reads from the
// APTEVA_SETUP_TOKEN env var. We mint the token here, inject it on
// spawn, and return it to the caller so the operator can finish
// registration via the setup URL. The capture-from-apteva.json path
// is gone: there is no api_key to read at spawn time.
//
// Caller owns persisting the row + handing the token to the operator.
// For respawn paths (tenant_start), pass an empty setupToken — the
// server keeps its first-boot token in the DB and ignores re-injection.
func (a *App) spawnTenant(ctx context.Context, slug, configDir, aptevaBin string, port int, setupToken string) (proc *tenantProc, err error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir configDir: %w", err)
	}
	bin, err := resolveAptevaBin(aptevaBin)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "--data-dir", configDir, "--port", strconv.Itoa(port), "--no-browser")
	env := append(os.Environ(),
		"APTEVA_HOME="+configDir,
		"PORT="+strconv.Itoa(port),
		"QUIET=1",
	)
	if setupToken != "" {
		// Server reads this on boot and uses it as the required
		// X-Setup-Token for the first /auth/register call. Without
		// this, the server mints its own random token, prints it to
		// stderr, and we'd have no way to surface it to the operator.
		env = append(env, "APTEVA_SETUP_TOKEN="+setupToken, "APTEVA_REGISTRATION=setup")
	}
	cmd.Env = env
	// New process group: child survives if fleet itself restarts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wire stdout/stderr into a logs file per tenant.
	logsPath := filepath.Join(configDir, "fleet-child.log")
	logFile, err := os.OpenFile(logsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open logs: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start apteva: %w", err)
	}
	proc = &tenantProc{cmd: cmd, port: port, started: time.Now()}

	// Reap on exit so a crashed child doesn't become a zombie.
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		a.procMu.Lock()
		// Only forget the proc if it's still the same one (avoid racing
		// a concurrent restart).
		if a.procs[slug] == proc {
			delete(a.procs, slug)
		}
		a.procMu.Unlock()
	}()

	// Wait for /api/health to come up. The CLI takes a few seconds
	// to boot server + core; the health endpoint is public so we
	// don't need an api_key to probe it.
	if err := waitForReady(ctx, port, 30*time.Second); err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return nil, fmt.Errorf("tenant did not become ready: %w", err)
	}
	return proc, nil
}

// mintSetupToken generates a fresh setup token in the same shape
// apteva-server itself uses ("apt_" + 32 hex chars). The server
// accepts whatever we pass via APTEVA_SETUP_TOKEN, but staying on
// the canonical shape keeps the operator-visible token recognizable
// against the stand-alone CLI's own setup banner.
func mintSetupToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint setup token: %w", err)
	}
	return "apt_" + hex.EncodeToString(b), nil
}

// stopTenant signals the tenant proc to exit. SIGTERM, wait, then KILL.
func stopProcess(p *tenantProc, grace time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// Process already gone — fine.
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}

// reconcileOnBoot probes every local tenant's port to recover state
// after a fleet restart. We don't try to re-attach PIDs — if the child
// survived (orphan in its own pgrp), the port is still bound and the
// tenant is treated as live; if not, it gets flipped to stopped.
func (a *App) reconcileOnBoot() error {
	tenants, err := a.store.list(map[string]string{"kind": KindLocal})
	if err != nil {
		return err
	}
	for _, t := range tenants {
		port, err := portFromBaseURL(t.BaseURL)
		if err != nil || port == 0 {
			continue
		}
		alive := portInUse(port)
		switch {
		case alive && t.Status == StatusStopped:
			_ = a.store.setStatus(t.ID, StatusActive, "worker:reconcile")
		case !alive && (t.Status == StatusActive || t.Status == StatusStarting || t.Status == StatusSetupPending):
			// setup_pending tenants whose process died mid-setup get
			// the same treatment as everything else — flip to stopped
			// so tenant_start respawns them; toolStart preserves the
			// setup_pending status across the respawn.
			_ = a.store.setStatus(t.ID, StatusStopped, "worker:reconcile")
		}
	}
	return nil
}

// portInUse reports whether something is bound to localhost:<port>. We
// try to bind and free immediately — if the bind fails with EADDRINUSE
// the port is in use.
func portInUse(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

// allocatePort picks a free port by binding to :0 and closing. Racy
// with anyone else binding between our close and the child's listen —
// acceptable for a single-host fleet on the parent's loopback.
func allocatePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForReady polls /api/health on the given port until it answers 200
// or the deadline passes.
func waitForReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", port)
	for {
		if time.Now().After(deadline) {
			return errors.New("timeout waiting for /api/health")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := httpClient.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// resolveAptevaBin finds the apteva CLI binary in this order:
//
//	1. explicit arg from tenant_create
//	2. FLEET_APTEVA_BIN env
//	3. `apteva` on $PATH (sidecar PATH may not include the npm install dir)
//	4. $HOME/.apteva/bin/apteva   — canonical npm-shim location
//	5. /usr/local/bin/apteva       — common Homebrew / manual install location
//	6. /opt/homebrew/bin/apteva    — Apple Silicon Homebrew default
//
// The sidecar process inherits PATH from apteva-server's launcher, which
// in practice often skips the user's npm bin dir. Adding well-known
// fallbacks lets the install work out of the box on a default setup.
func resolveAptevaBin(explicit string) (string, error) {
	candidates := []string{explicit, os.Getenv("FLEET_APTEVA_BIN")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".apteva", "bin", "apteva"))
	}
	candidates = append(candidates, "/usr/local/bin/apteva", "/opt/homebrew/bin/apteva")

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if p, err := exec.LookPath("apteva"); err == nil {
		return p, nil
	}
	return "", errors.New("apteva binary not found — set FLEET_APTEVA_BIN or pass apteva_bin (tried PATH, ~/.apteva/bin/apteva, /usr/local/bin/apteva, /opt/homebrew/bin/apteva)")
}

// portFromBaseURL extracts the port from "http://localhost:5301".
// Returns 0 when no port is present (e.g., https without explicit
// port — out of scope for local tenants but doesn't error).
func portFromBaseURL(baseURL string) (int, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return 0, err
	}
	p := u.Port()
	if p == "" {
		return 0, nil
	}
	return strconv.Atoi(p)
}

// slugDataDir returns the per-tenant data directory under the fleet
// data root. Slug is validated to filesystem-safe characters.
func slugDataDir(slug string) (string, error) {
	if slug == "" {
		return "", errors.New("slug required")
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return "", fmt.Errorf("slug must be [a-z0-9_-], got %q", slug)
		}
	}
	if strings.HasPrefix(slug, "-") || strings.HasPrefix(slug, "_") {
		return "", errors.New("slug must not start with - or _")
	}
	return filepath.Join(localDataRoot(), slug), nil
}
