package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
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
// In --no-browser mode the apteva CLI deliberately doesn't auto-
// register an admin user — registration happens in the browser
// using a setup token apteva-server prints on stderr during its
// first boot. The CLI re-prints the same token to its own banner
// (cli.log + our captured fleet-child.log), so we scrape it from
// there once /api/health responds.
//
// Why we don't pass APTEVA_SETUP_TOKEN ourselves: the apteva CLI's
// process-spawn path doesn't propagate that env to its apteva-server
// child (the CLI's exec strips fleet's env). The server then mints
// its own random token and ignores anything we set. Scraping the
// real value is the workaround until the CLI propagates that env.
//
// For respawn paths (tenant_start, pass freshSetup=false) we don't
// look for a token — the tenant already has a users table from its
// first boot, registration is locked.
func (a *App) spawnTenant(ctx context.Context, tenantID, slug, configDir, aptevaBin string, port int, freshSetup bool) (setupToken string, proc *tenantProc, err error) {
	return a.spawnTenantWithMode(ctx, tenantID, slug, configDir, aptevaBin, port, freshSetup, false)
}

func (a *App) spawnTenantWithMode(ctx context.Context, tenantID, slug, configDir, aptevaBin string, port int, freshSetup, quarantine bool) (setupToken string, proc *tenantProc, err error) {
	if _, err := validatedTenantSlug(slug); err != nil {
		return "", nil, err
	}
	if err := validateLocalTenantDir(slug, configDir); err != nil {
		return "", nil, err
	}
	if err := validateTenantPort(port); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("mkdir configDir: %w", err)
	}
	bin, err := resolveAptevaBin(aptevaBin)
	if err != nil {
		return "", nil, err
	}
	cmd := buildSpawnCmd(slug, bin,
		[]string{"--data-dir", configDir, "--port", strconv.Itoa(port), "--no-browser"})
	cmd.Env = tenantSpawnEnvForMode(configDir, port, tenantID, quarantine)
	// New process group: child survives if fleet itself restarts.
	// (Setpgid is also set redundantly inside buildSpawnCmd; harmless.)
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

	// Wait for /api/health to come up. The CLI takes a few seconds
	// to boot server + core; the health endpoint is public so we
	// don't need an api_key to probe it.
	if err := waitForReady(ctx, port, 30*time.Second); err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return "", nil, fmt.Errorf("tenant did not become ready: %w", err)
	}

	if freshSetup {
		// Scrape the setup token from the CLI's first-boot banner.
		// It's there by the time /api/health responds; if it isn't,
		// give the CLI a brief grace period (its log write can lag
		// the readiness response by a few hundred ms).
		token, scrapeErr := scrapeSetupToken(logsPath, 5*time.Second)
		if scrapeErr != nil {
			_ = stopProcess(proc, 2*time.Second)
			return "", nil, fmt.Errorf("scrape setup token: %w", scrapeErr)
		}
		return token, proc, nil
	}
	return "", proc, nil
}

func tenantSpawnEnv(configDir string, port int, tenantID string) []string {
	deployStart, deployEnd, codeStart, codeEnd := tenantAppPortRanges(port)
	env := append([]string{}, os.Environ()...)
	env = setEnv(env, "APTEVA_HOME", configDir)
	env = setEnv(env, "PORT", strconv.Itoa(port))
	env = setEnv(env, "QUIET", "1")
	env = setEnv(env, "APTEVA_INGRESS_ENABLED", "0")
	env = setEnv(env, "APTEVA_HTTP_LISTEN_ADDR", "")
	env = setEnv(env, "APTEVA_HTTPS_LISTEN_ADDR", "")
	env = setEnv(env, "DEPLOY_RELEASE_PORT_RANGE_START", strconv.Itoa(deployStart))
	env = setEnv(env, "DEPLOY_RELEASE_PORT_RANGE_END", strconv.Itoa(deployEnd))
	env = setEnv(env, "CODE_DEV_PORT_RANGE_START", strconv.Itoa(codeStart))
	env = setEnv(env, "CODE_DEV_PORT_RANGE_END", strconv.Itoa(codeEnd))
	if fleetPort := strings.TrimSpace(os.Getenv("APTEVA_APP_PORT")); fleetPort != "" {
		env = setEnv(env, "APTEVA_DELEGATED_DNS_FLEET_URL", "http://127.0.0.1:"+fleetPort)
	}
	if token := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN")); token != "" {
		env = setEnv(env, "APTEVA_DELEGATED_DNS_TOKEN", token)
	}
	if projectID := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); projectID != "" {
		env = setEnv(env, "APTEVA_DELEGATED_DNS_PROJECT_ID", projectID)
	}
	if tenantID = strings.TrimSpace(tenantID); tenantID != "" {
		env = setEnv(env, "APTEVA_DELEGATED_DNS_TENANT_ID", tenantID)
	}
	return env
}

func tenantSpawnEnvForMode(configDir string, port int, tenantID string, quarantine bool) []string {
	env := tenantSpawnEnv(configDir, port, tenantID)
	if quarantine {
		env = setEnv(env, "APTEVA_CLONE_QUARANTINE", "1")
	} else {
		env = setEnv(env, "APTEVA_CLONE_QUARANTINE", "0")
	}
	return env
}

func tenantAppPortRanges(port int) (deployStart, deployEnd, codeStart, codeEnd int) {
	base := port + 1000
	if base < 1024 || base+999 > 65535 {
		base = 20000 + (port%30)*1000
	}
	return base, base + 899, base + 900, base + 999
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// setupTokenRe matches the canonical apteva setup token shape that
// apteva-server prints on stderr ("apt_" + 32 lowercase hex chars)
// and the apteva CLI re-prints in its first-run banner.
var setupTokenRe = regexp.MustCompile(`apt_[0-9a-f]{32}`)

// scrapeSetupToken reads the captured CLI log and returns the first
// setup token it finds. Polls with backoff up to timeout because the
// CLI may write its banner asynchronously after /api/health goes 200.
func scrapeSetupToken(logPath string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		raw, err := os.ReadFile(logPath)
		if err == nil {
			if m := setupTokenRe.Find(raw); m != nil {
				return string(m), nil
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("setup token did not appear in CLI log within " + timeout.String() + " (apteva CLI banner format may have changed)")
		}
		time.Sleep(150 * time.Millisecond)
	}
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

// stopTenantBy makes "stop this tenant's process" work even when
// fleet's in-memory procs handle is empty — e.g., after a fleet
// upgrade re-spawned the sidecar but left the tenant running.
//
// The important safety rule is: never signal a process group inferred
// from a port listener. In production a listener's process group can
// cross an unexpected boundary and include the parent apteva service.
// Instead we stop the tenant's own systemd scope when available, then
// fall back to a validated process tree whose command line contains
// the tenant data dir marker.
//
// Returns nil if no process is on the port (already stopped). Errors
// only when a listener remains but we cannot prove how to stop it
// without risking unrelated processes.
func (a *App) stopTenantBy(slug, configDir string, port int, grace time.Duration) error {
	if _, err := validatedTenantSlug(slug); err != nil {
		return err
	}
	if configDir != "" {
		if err := validateLocalTenantDir(slug, configDir); err != nil {
			return err
		}
	}
	if port > 0 {
		if err := validateTenantPort(port); err != nil {
			return err
		}
	}
	a.procMu.Lock()
	p := a.procs[slug]
	if p != nil {
		// Clear the handle eagerly; we own the kill now.
		delete(a.procs, slug)
	}
	a.procMu.Unlock()

	if stopped, err := stopTenantScopes(slug, port, grace); err != nil {
		return err
	} else if stopped {
		return nil
	}

	marker := tenantProcessMarker(slug, configDir)
	if p != nil && p.cmd != nil && p.cmd.Process != nil {
		if stopped, err := stopTenantProcessTree(p.cmd.Process.Pid, marker, port, grace); err != nil {
			return err
		} else if stopped {
			return nil
		}
	}

	if port <= 0 {
		return nil
	}
	pid, _ := findPidOnPort(port)
	if pid <= 0 {
		return nil // nothing to stop
	}
	if stopped, err := stopTenantProcessTree(pid, marker, port, grace); err != nil {
		return err
	} else if stopped {
		return nil
	}
	if port > 0 && portInUse(port) {
		return fmt.Errorf("pid %d still listening on :%d, but no tenant-owned scope or process tree matched %q", pid, port, marker)
	}
	return nil
}

func tenantProcessMarker(slug, configDir string) string {
	if marker := strings.TrimSpace(configDir); marker != "" {
		return marker
	}
	return string(os.PathSeparator) + ".apteva-fleet" + string(os.PathSeparator) + sanitizeUnit(slug)
}

func tenantScopePattern(slug string) string {
	return "fleet-tenant-" + sanitizeUnit(slug) + "*.scope"
}

func stopTenantScopes(slug string, port int, grace time.Duration) (bool, error) {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil || systemctl == "" {
		return false, nil
	}
	units, err := listTenantScopeUnits(systemctl, slug)
	if err != nil {
		return false, err
	}
	if len(units) == 0 {
		return false, nil
	}
	args := append([]string{"kill", "--signal=TERM"}, units...)
	_ = exec.Command(systemctl, args...).Run()
	if waitForPortFree(port, grace) {
		return true, nil
	}
	args = append([]string{"kill", "--signal=KILL"}, units...)
	_ = exec.Command(systemctl, args...).Run()
	if waitForPortFree(port, 5*time.Second) {
		return true, nil
	}
	return true, fmt.Errorf("tenant scope(s) %s still listening on :%d after SIGKILL", strings.Join(units, ","), port)
}

func listTenantScopeUnits(systemctl, slug string) ([]string, error) {
	out, err := exec.Command(systemctl, "list-units", "--all", "--plain", "--no-legend", tenantScopePattern(slug)).Output()
	if err != nil {
		return nil, fmt.Errorf("list tenant scopes: %w", err)
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		base := "fleet-tenant-" + sanitizeUnit(slug)
		if unit == base+".scope" || (strings.HasPrefix(unit, base+"-") && strings.HasSuffix(unit, ".scope")) {
			units = append(units, unit)
		}
	}
	return units, nil
}

type procInfo struct {
	pid     int
	ppid    int
	cmdline string
	args    []string
}

func stopTenantProcessTree(seedPID int, marker string, port int, grace time.Duration) (bool, error) {
	if seedPID <= 0 {
		return false, nil
	}
	procs, err := readProcTable()
	if err != nil {
		return false, nil // /proc is Linux-only; callers may still be on macOS.
	}
	root, ok := tenantTreeRoot(seedPID, marker, procs)
	if !ok {
		return false, nil
	}
	pids := procTreePIDs(root, procs)
	signalPIDs(pids, syscall.SIGTERM)
	if waitForPortFree(port, grace) {
		return true, nil
	}
	signalPIDs(pids, syscall.SIGKILL)
	if waitForPortFree(port, 5*time.Second) {
		return true, nil
	}
	return true, fmt.Errorf("tenant process tree rooted at pid %d still listening on :%d after SIGKILL", root, port)
}

func readProcTable() (map[int]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return readPSProcTable()
	}
	out := make(map[int]procInfo)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, ok := parseProcPPID(string(stat))
		if !ok {
			continue
		}
		rawCmd, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		parts := strings.Split(strings.TrimSuffix(string(rawCmd), "\x00"), "\x00")
		cmdline := strings.Join(parts, " ")
		out[pid] = procInfo{pid: pid, ppid: ppid, cmdline: strings.TrimSpace(cmdline), args: parts}
	}
	return out, nil
}

// readPSProcTable is the conservative fallback for macOS and other systems
// without procfs. It preserves argument boundaries only for paths without
// whitespace; processHasExactTenantArg therefore refuses ambiguous data-dir
// paths instead of falling back to substring matching.
func readPSProcTable() (map[int]procInfo, error) {
	ps, err := exec.LookPath("ps")
	if err != nil {
		return nil, err
	}
	raw, err := exec.Command(ps, "-ww", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("read process table: %w", err)
	}
	return parsePSProcTable(string(raw)), nil
}

func parsePSProcTable(raw string) map[int]procInfo {
	out := make(map[int]procInfo)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			continue
		}
		args := append([]string(nil), fields[2:]...)
		out[pid] = procInfo{pid: pid, ppid: ppid, cmdline: strings.Join(args, " "), args: args}
	}
	return out
}

func parseProcPPID(stat string) (int, bool) {
	end := strings.LastIndex(stat, ")")
	if end < 0 || end+2 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}

func tenantTreeRoot(seedPID int, marker string, procs map[int]procInfo) (int, bool) {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return 0, false
	}
	seen := map[int]bool{}
	root := 0
	for pid := seedPID; pid > 1 && !seen[pid]; {
		seen[pid] = true
		p, ok := procs[pid]
		if !ok {
			break
		}
		if processHasExactTenantArg(p, marker) {
			root = pid
		}
		pid = p.ppid
	}
	if root == 0 {
		return 0, false
	}
	return root, true
}

func processHasExactTenantArg(p procInfo, marker string) bool {
	args := p.args
	if len(args) == 0 {
		args = strings.Fields(p.cmdline)
	}
	for _, arg := range args {
		if arg == marker || arg == "--data-dir="+marker {
			return true
		}
	}
	return false
}

func procTreePIDs(root int, procs map[int]procInfo) []int {
	children := map[int][]int{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	for ppid := range children {
		sort.Ints(children[ppid])
	}
	var out []int
	var walk func(int)
	walk = func(pid int) {
		for _, child := range children[pid] {
			walk(child)
		}
		out = append(out, pid)
	}
	walk(root)
	return out
}

func signalPIDs(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if pid > 1 {
			_ = syscall.Kill(pid, sig)
		}
	}
}

func waitForPortFree(port int, timeout time.Duration) bool {
	if port == 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if !portInUse(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// findPidOnPort returns the pid of the process LISTENING on the given
// TCP port on the loopback / wildcard, or 0+err if no listener found.
// Tries `lsof` first (works on macOS + Linux when installed), then
// `ss` (Linux). Pure-Go /proc parsing is the next fallback to add if
// neither is present.
func findPidOnPort(port int) (int, error) {
	if lsof, err := exec.LookPath("lsof"); err == nil {
		// -ti returns pids only, one per line; -sTCP:LISTEN excludes
		// connected sockets owned by random clients.
		out, _ := exec.Command(lsof, "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && n > 0 {
				return n, nil
			}
		}
	}
	if ss, err := exec.LookPath("ss"); err == nil {
		// `ss -ltnpH 'sport = :PORT'` → users:(("name",pid=X,fd=Y))
		out, _ := exec.Command(ss, "-ltnpH", fmt.Sprintf("sport = :%d", port)).Output()
		if len(bytes.TrimSpace(out)) > 0 {
			re := regexp.MustCompile(`pid=(\d+)`)
			if m := re.FindSubmatch(out); len(m) >= 2 {
				if n, err := strconv.Atoi(string(m[1])); err == nil && n > 0 {
					return n, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("no listener found on port %d (and neither lsof nor ss returned a pid)", port)
}

// reconcileOnBoot probes every local tenant's port to recover state
// after a fleet restart. Two paths:
//
//   - port still bound → tenant survived (its own pgrp, or — better —
//     its own systemd-run scope outside our cgroup); just unstale the
//     status. Reset the respawn counter since we're observing healthy.
//   - port empty → process died (systemd cgroup kill on parent
//     restart, crash, oom). For active/starting tenants we ATTEMPT to
//     respawn instead of flipping to stopped. setup_pending tenants
//     also get respawned (toolStart preserves their status).
func (a *App) reconcileOnBoot(app *sdk.AppCtx) error {
	tenants, err := a.store.list(map[string]string{"kind": KindLocal})
	if err != nil {
		return err
	}
	ctx := context.Background()
	var reconcileErrs []error
	for _, t := range tenants {
		if a.tenantOperation(t.ID) != "" {
			continue
		}
		port, err := portFromBaseURL(t.BaseURL)
		if err != nil || port == 0 {
			continue
		}
		if t.IsHosted() {
			if reconcileErr := a.reconcileHostedOnBoot(ctx, app, t, port); reconcileErr != nil {
				_ = a.store.recordEvent(t.ID, "hosted_reconcile_failed", "worker:reconcile",
					map[string]any{"error": reconcileErr.Error(), "instance_id": t.InstanceID})
				reconcileErrs = append(reconcileErrs, fmt.Errorf("tenant %s: %w", t.ID, reconcileErr))
			}
			continue
		}
		alive := portInUse(port)
		switch {
		case alive:
			if t.Status == StatusStopped {
				_ = a.store.setStatus(t.ID, StatusActive, "worker:reconcile")
			}
			_ = a.store.resetRespawn(t.ID)
		case !alive && (t.Status == StatusActive || t.Status == StatusStarting || t.Status == StatusSetupPending):
			a.tryRespawn(ctx, t)
		}
	}
	return errors.Join(reconcileErrs...)
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
//  1. explicit arg from tenant_create
//  2. FLEET_APTEVA_BIN env
//  3. `apteva` on $PATH (sidecar PATH may not include the npm install dir)
//  4. $HOME/.apteva/bin/apteva   — canonical npm-shim location
//  5. /usr/local/bin/apteva       — common Homebrew / manual install location
//  6. /opt/homebrew/bin/apteva    — Apple Silicon Homebrew default
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

// detectPublicHost returns the IP the host uses to reach the public
// internet. Mechanism: dial UDP to 8.8.8.8:80, read back the local
// addr the kernel chose. No packets actually go over the wire — UDP
// "Dial" is just a route-resolution that fills the local addr field.
//
// On a single-NIC Hetzner/DO box this returns the public IPv4. On a
// dev laptop it returns the LAN address (e.g. 192.168.1.42). On a
// completely offline / IPv6-only host the Dial fails and we fall
// back to "localhost" — better than a confusing wrong value.
//
// Operators on multi-homed hosts where the auto-detected interface
// isn't the right one should look at v0.3's planned FLEET_PUBLIC_HOST
// override; v0.2.x sticks to pure auto-detect per design pick.
func detectPublicHost() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "localhost"
	}
	return addr.IP.String()
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

// buildSpawnCmd constructs the *exec.Cmd that launches a tenant. On
// systemd hosts it wraps the call in `systemd-run --scope` so the
// tenant lands in its own transient scope cgroup, outside the
// fleet/apteva.service cgroup. That way a `systemctl restart apteva`
// (default KillMode=control-group) doesn't take the tenant with it.
//
// Fallback to plain exec.Command when systemd-run isn't on PATH (dev
// machines, macOS, non-systemd Linux); then survival depends on
// reconcileOnBoot + auto-respawn instead.
//
// The unit name is fleet-tenant-<slug>.scope. --collect lets systemd
// remove the scope's residue once the process exits without an
// explicit `systemctl reset-failed`.
func buildSpawnCmd(slug, bin string, args []string) *exec.Cmd {
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil || systemdRun == "" {
		return exec.Command(bin, args...)
	}
	// `systemd-run --scope`: foreground; PID is the actual command,
	// cmd.Wait() works as if exec was direct. Quiet to keep our
	// captured stderr clean.
	//
	// Unit name: include a unix-nano suffix so re-spawns never collide
	// with a stale scope. The old fixed name (fleet-tenant-<slug>) hit
	// "Unit ... was already loaded" on every tenant_update because the
	// pre-update scope's last process hadn't fully exited yet when
	// the new spawn tried to register. --collect cleans up stopped
	// scopes, but the cleanup is asynchronous; a unique name dodges
	// the race entirely.
	unit := fmt.Sprintf("fleet-tenant-%s-%d", sanitizeUnit(slug), time.Now().UnixNano())
	full := append([]string{
		"--quiet", "--collect", "--scope", "--unit=" + unit, "--", bin,
	}, args...)
	return exec.Command(systemdRun, full...)
}

// sanitizeUnit replaces anything outside [a-zA-Z0-9-_] with '_' so
// the slug is a legal systemd unit name suffix.
func sanitizeUnit(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// tryRespawn is called from the health poller when it sees an active
// local tenant whose port is empty. It bumps the per-tenant counter,
// respawns up to maxRespawnAttempts within the recent window, and
// flips to failed beyond that to avoid a crash loop.
const (
	maxRespawnAttempts = 5
	respawnGraceTime   = 30 * time.Second // min wait between respawns
)

func (a *App) tryRespawn(ctx context.Context, t *Tenant) {
	if t.Kind != KindLocal || t.IsHosted() || a.tenantOperation(t.ID) != "" {
		return
	}
	// Respect a small grace so we don't hammer a tenant that's mid-boot.
	if t.LastRespawnAt != nil && time.Since(*t.LastRespawnAt) < respawnGraceTime {
		return
	}
	if t.RespawnAttempts >= maxRespawnAttempts {
		if t.Status != StatusFailed {
			_ = a.store.setStatus(t.ID, StatusFailed, "worker:auto_respawn")
			_ = a.store.recordEvent(t.ID, "auto_respawn_gave_up", "worker:auto_respawn",
				map[string]any{"attempts": t.RespawnAttempts})
		}
		return
	}
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 || validateLocalTenantDir(t.Slug, t.ConfigDir) != nil {
		// Can't respawn without these; surface and stop trying for now.
		_ = a.store.setStatus(t.ID, StatusFailed, "worker:auto_respawn")
		_ = a.store.recordEvent(t.ID, "auto_respawn_failed", "worker:auto_respawn",
			map[string]any{"reason": "missing port or config_dir"})
		return
	}
	_ = a.store.bumpRespawn(t.ID)
	// Use the pinned version if set, else default-resolve. An empty
	// path falls back transparently inside resolveAptevaBin.
	_, proc, err := a.spawnTenant(ctx, t.ID, t.Slug, t.ConfigDir, tenantAptevaBin(t.TargetVersion), port, false)
	if err != nil {
		_ = a.store.recordEvent(t.ID, "auto_respawn_failed", "worker:auto_respawn",
			map[string]any{"error": err.Error()})
		return
	}
	a.procMu.Lock()
	a.procs[t.Slug] = proc
	a.procMu.Unlock()
	_ = a.store.setStatus(t.ID, StatusActive, "worker:auto_respawn")
	_ = a.store.recordEvent(t.ID, "auto_respawn_ok", "worker:auto_respawn",
		map[string]any{"port": port})
}

func (a *App) tryRespawnHosted(ctx context.Context, app *sdk.AppCtx, t *Tenant) {
	if t == nil || !t.IsHosted() {
		return
	}
	done, err := a.beginTenantOperation(t.ID, "hosted auto-respawn")
	if err != nil {
		return
	}
	defer done()
	if t.LastRespawnAt != nil && time.Since(*t.LastRespawnAt) < respawnGraceTime {
		return
	}
	if t.RespawnAttempts >= maxRespawnAttempts {
		_ = a.store.setStatus(t.ID, StatusFailed, "worker:auto_respawn")
		_ = a.store.recordEvent(t.ID, "auto_respawn_gave_up", "worker:auto_respawn",
			map[string]any{"attempts": t.RespawnAttempts, "instance_id": t.InstanceID})
		return
	}
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 || validateHostedTenantDir(t.Slug, t.ConfigDir) != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "worker:auto_respawn")
		_ = a.store.recordEvent(t.ID, "auto_respawn_failed", "worker:auto_respawn",
			map[string]any{"reason": "invalid port or config_dir", "instance_id": t.InstanceID})
		return
	}
	info, err := a.getInstanceInfo(app, t.InstanceID)
	if err != nil {
		_ = a.store.recordEvent(t.ID, "auto_respawn_failed", "worker:auto_respawn", map[string]any{"error": err.Error()})
		return
	}
	_ = a.store.bumpRespawn(t.ID)
	_, _, err = a.spawnHostedTenant(app, hostedSpawnSpecForTenant(t, info.PublicIPv4, port))
	if err != nil {
		_ = a.store.recordEvent(t.ID, "auto_respawn_failed", "worker:auto_respawn", map[string]any{"error": err.Error()})
		return
	}
	_ = a.store.setStatus(t.ID, statusAfterRestart(t.Status), "worker:auto_respawn")
	_ = a.store.recordEvent(t.ID, "auto_respawn_ok", "worker:auto_respawn",
		map[string]any{"port": port, "instance_id": t.InstanceID})
	_ = ctx
}

// slugDataDir returns the per-tenant data directory under the fleet
// data root. Slug is validated to filesystem-safe characters.
func slugDataDir(slug string) (string, error) {
	valid, err := validatedTenantSlug(slug)
	if err != nil {
		return "", err
	}
	if slug != valid {
		return "", errors.New("slug must already be in canonical lowercase form")
	}
	return filepath.Join(localDataRoot(), valid), nil
}
