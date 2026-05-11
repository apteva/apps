package main

import (
	"context"
	"encoding/json"
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
// Returns the api_key the CLI wrote to <configDir>/apteva.json once
// /api/health responds. Caller owns persisting the tenant row.
func (a *App) spawnTenant(ctx context.Context, slug, configDir, aptevaBin string, port int) (apiKey string, proc *tenantProc, err error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("mkdir configDir: %w", err)
	}
	bin, err := resolveAptevaBin(aptevaBin)
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command(bin, "--data-dir", configDir, "--port", strconv.Itoa(port), "--no-browser")
	cmd.Env = append(os.Environ(),
		"APTEVA_HOME="+configDir,
		"PORT="+strconv.Itoa(port),
		"QUIET=1",
	)
	// New process group: child survives if fleet itself restarts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wire stdout/stderr into a logs file per tenant.
	logsPath := filepath.Join(configDir, "fleet-child.log")
	logFile, err := os.OpenFile(logsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("open logs: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return "", nil, fmt.Errorf("start apteva: %w", err)
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

	// Wait for /api/health to come up. The CLI takes a few seconds to
	// boot server + core + write apteva.json.
	if err := waitForReady(ctx, port, 30*time.Second); err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return "", nil, fmt.Errorf("tenant did not become ready: %w", err)
	}
	key, err := readAPIKey(configDir)
	if err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return "", nil, fmt.Errorf("read api_key from %s/apteva.json: %w", configDir, err)
	}
	return key, proc, nil
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
		case !alive && (t.Status == StatusActive || t.Status == StatusStarting):
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

// readAPIKey extracts api_key from <configDir>/apteva.json. The CLI
// writes this file on first boot.
func readAPIKey(configDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, "apteva.json"))
	if err != nil {
		return "", err
	}
	var parsed struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.APIKey == "" {
		return "", errors.New("apteva.json has empty api_key")
	}
	return parsed.APIKey, nil
}

// resolveAptevaBin finds the apteva CLI binary in this order:
//
//	1. explicit arg from tenant_create
//	2. FLEET_APTEVA_BIN env
//	3. `apteva` on $PATH
func resolveAptevaBin(explicit string) (string, error) {
	for _, candidate := range []string{explicit, os.Getenv("FLEET_APTEVA_BIN")} {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("apteva"); err == nil {
		return p, nil
	}
	return "", errors.New("apteva binary not found — set FLEET_APTEVA_BIN or pass apteva_bin")
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
