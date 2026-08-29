package main

// Hosted-tenant supervisor — apteva-server processes running on a
// remote VPS managed by the Instances app, driven entirely through
// `instances.instance_run_command`. Mirror of localproc.go's
// responsibilities (spawn / probe / scrape / stop / version-install),
// but every action is a shell command executed over SSH via the
// platform-mediated integration.
//
// Disk layout on the remote VPS:
//
//	/var/lib/apteva-fleet/
//	  versions/<v>/node_modules/.bin/apteva    # per-version cache
//	  <slug>/                                  # tenant data dir
//	    apteva.db, apps/, fleet-child.log, ...
//
// Port allocation: fleet doesn't ask the VPS for a free port (would
// race; SSH round-trip too slow per-create). Operator picks the port
// at tenant_create time (default 7100 + tenant-count-on-instance), or
// passes one explicitly. v0.6.0 ships single-tenant-per-VPS as the
// happy path; multi-tenant packing is v0.7.
//
// No systemd-run --scope: the apteva-server runs under `setsid` so it
// detaches from the SSH session and survives the connection drop.
// Operator-driven stop kills the whole process tree via pid+pgrp.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	// Default port for the first hosted tenant on a VPS. Subsequent
	// tenants on the same instance get +1 — operator-collision-aware
	// via the explicit port arg if they want something specific.
	defaultHostedTenantPort = 7100

	// Remote disk roots. Hardcoded for v0.6; per-instance override
	// could land later if customers care about /opt vs /var/lib.
	remoteFleetRoot = "/var/lib/apteva-fleet"

	// Per-command SSH timeout. instance_run_command has its own
	// default but the operator might have set it higher; cap here so
	// a hung command doesn't block tenant_create indefinitely.
	hostedCmdTimeoutS = 60
)

// instanceBindingFor returns the bound Instances integration, or an
// error if it isn't installed/bound. Hosted tenants are impossible
// without it.
func (a *App) instanceBindingFor(ctx *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	if ctx == nil {
		return nil, errors.New("no platform context")
	}
	b := ctx.IntegrationFor("host_provider")
	if b == nil || b.Kind != "app" {
		return nil, errors.New("instances app not bound to host_provider role — install instances and bind it on fleet")
	}
	return b, nil
}

// instanceInfo is the projection of an Instance row we need to drive
// a hosted tenant. Resolved at create + at every stop/update because
// IPs can shift if the operator destroys/replaces a VPS.
type instanceInfo struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	PublicIPv4 string `json:"public_ipv4"`
	Status     string `json:"status"`
	SSHUser    string `json:"ssh_user,omitempty"`
	SSHHost    string `json:"ssh_host,omitempty"`
	SSHPort    int    `json:"ssh_port,omitempty"`
}

func (a *App) getInstanceInfo(ctx *sdk.AppCtx, id int64) (*instanceInfo, error) {
	if id <= 0 {
		return nil, errors.New("instance_id must be > 0 for a hosted tenant")
	}
	var out struct {
		Instance instanceInfo `json:"instance"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_get",
		map[string]any{"id": id}, &out); err != nil {
		return nil, fmt.Errorf("instances.instance_get(%d): %w", id, err)
	}
	if out.Instance.ID == 0 {
		return nil, fmt.Errorf("instance %d not found", id)
	}
	if out.Instance.Status != "ready" {
		return nil, fmt.Errorf("instance %d not ready (status=%s)", id, out.Instance.Status)
	}
	if out.Instance.PublicIPv4 == "" {
		return nil, fmt.Errorf("instance %d has no public_ipv4", id)
	}
	return &out.Instance, nil
}

// instanceRunCommand is the workhorse — every hosted-side action goes
// through this. Returns combined stdout+stderr and the exit code so
// callers can branch on either signal.
func instanceRunCommand(ctx *sdk.AppCtx, instanceID int64, cmd string, timeoutS int) (string, int, error) {
	if timeoutS <= 0 {
		timeoutS = hostedCmdTimeoutS
	}
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error,omitempty"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_run_command",
		map[string]any{
			"id":        instanceID,
			"cmd":       cmd,
			"timeout_s": timeoutS,
		}, &out); err != nil {
		return "", -1, fmt.Errorf("instance_run_command: %w", err)
	}
	if out.Error != "" {
		return out.Output, out.ExitCode, errors.New(out.Error)
	}
	return out.Output, out.ExitCode, nil
}

// hostedSpawnSpec captures everything spawnHosted needs. Built from
// toolCreate's args + a fresh resolution of the target instance.
type hostedSpawnSpec struct {
	InstanceID  int64
	InstanceIP  string
	Slug        string
	Port        int
	AptevaVer   string // npm version, e.g. "0.17.3"
	FreshSetup  bool   // first boot → scrape setup_token from log
	Quarantine  bool   // management API only; do not activate copied workloads
	IngressMode string
	PrimaryHost string
	ACMEEmail   string
}

func hostedSpawnSpecForTenant(t *Tenant, instanceIP string, port int) hostedSpawnSpec {
	spec := hostedSpawnSpec{
		InstanceID:  t.InstanceID,
		InstanceIP:  instanceIP,
		Slug:        t.Slug,
		Port:        port,
		AptevaVer:   tenantVersion(t),
		IngressMode: t.IngressMode,
		PrimaryHost: t.Domain,
		ACMEEmail:   hostedACMEEmail(),
	}
	return spec
}

func hostedACMEEmail() string {
	if email := strings.TrimSpace(os.Getenv("FLEET_ACME_EMAIL")); email != "" {
		return email
	}
	return strings.TrimSpace(os.Getenv("APTEVA_ACME_EMAIL"))
}

func hostedProcessEnv(spec hostedSpawnSpec) string {
	directIngress := !spec.Quarantine && (spec.IngressMode == IngressDirectPending || spec.IngressMode == IngressDirect)
	if !directIngress {
		return "APTEVA_BIND=127.0.0.1 APTEVA_INGRESS_ENABLED=0 APTEVA_HTTP_LISTEN_ADDR= APTEVA_HTTPS_LISTEN_ADDR="
	}
	return fmt.Sprintf(
		"APTEVA_BIND=127.0.0.1 APTEVA_INGRESS_ENABLED=1 APTEVA_HTTP_LISTEN_ADDR=:80 APTEVA_HTTPS_LISTEN_ADDR=:443 APTEVA_PRIMARY_HOST=%s APTEVA_ACME_EMAIL=%s",
		sh(spec.PrimaryHost), sh(spec.ACMEEmail),
	)
}

// spawnHostedTenant boots a fresh apteva-server on the remote VPS.
// Steps:
//
//  1. mkdir the tenant data dir
//  2. npm-install apteva@<v> into <root>/versions/<v>/ if not cached
//  3. setsid the apteva CLI in background, redirecting its log
//  4. poll http://<vps-ip>:<port>/api/health for readiness
//  5. if freshSetup, tail the log for the setup_token banner
//
// Returns the scraped setup_token (empty for respawn paths) and the
// remote base_url. No process handle — there's nothing fleet can do
// locally to manage a remote PID; subsequent stop/update use
// instanceRunCommand again.
// Bounded latency comes from the inner per-call timeouts on
// instance_run_command (10–180s each) plus waitForRemoteReady's own
// 60s probe budget — no outer context.WithTimeout needed.
func (a *App) spawnHostedTenant(ctx *sdk.AppCtx, spec hostedSpawnSpec) (setupToken, baseURL string, err error) {
	if spec.InstanceID == 0 || spec.InstanceIP == "" || spec.Slug == "" || spec.Port == 0 {
		return "", "", errors.New("hosted spawn: instance_id, instance_ip, slug, port all required")
	}
	if _, err := validatedTenantSlug(spec.Slug); err != nil {
		return "", "", err
	}
	if err := validateTenantPort(spec.Port); err != nil {
		return "", "", err
	}
	directIngress := !spec.Quarantine && (spec.IngressMode == IngressDirectPending || spec.IngressMode == IngressDirect)
	if directIngress {
		primaryHost, hostErr := normaliseExactHostname(spec.PrimaryHost)
		if hostErr != nil {
			return "", "", fmt.Errorf("hosted direct ingress requires a primary domain: %w", hostErr)
		}
		spec.PrimaryHost = primaryHost
	}
	if err := a.ensureHostedRuntime(ctx, spec.InstanceID); err != nil {
		return "", "", err
	}
	if spec.AptevaVer == "" {
		// Network-failure tolerant: fall back to "latest" string, the
		// remote npm install will resolve it.
		spec.AptevaVer = "latest"
	}
	spec.AptevaVer, err = validateAptevaVersion(spec.AptevaVer, false)
	if err != nil {
		return "", "", err
	}

	dataDir := remoteFleetRoot + "/" + spec.Slug
	versionDir := remoteFleetRoot + "/versions/" + spec.AptevaVer
	binPath := versionDir + "/node_modules/.bin/apteva"
	logPath := dataDir + "/fleet-child.log"
	pidPath := dataDir + "/fleet.pid"

	// 1) mkdir + 2) npm install (idempotent — npm install is a no-op
	//    if already present and we want the same version).
	if _, code, err := instanceRunCommand(ctx, spec.InstanceID,
		fmt.Sprintf(`mkdir -p %s %s`, sh(dataDir), sh(versionDir)),
		30,
	); err != nil || code != 0 {
		return "", "", fmt.Errorf("mkdir remote dirs: %w (exit %d)", err, code)
	}
	// Test for the binary first to skip a 30s+ npm install on cache hit.
	if _, code, _ := instanceRunCommand(ctx, spec.InstanceID,
		fmt.Sprintf(`test -x %s`, sh(binPath)), 5,
	); code != 0 {
		ctx.Logger().Info("hosted: installing apteva on instance",
			"instance_id", spec.InstanceID, "version", spec.AptevaVer)
		if _, code, err := instanceRunCommand(ctx, spec.InstanceID,
			fmt.Sprintf(`npm install --prefix %s --no-audit --no-fund --silent apteva@%s`,
				sh(versionDir), sh(spec.AptevaVer)),
			180, // npm install on a cold VPS can take a while
		); err != nil || code != 0 {
			return "", "", fmt.Errorf("npm install apteva@%s: %w (exit %d)", spec.AptevaVer, err, code)
		}
	}

	// 3) Spawn under setsid so the process survives the SSH session drop.
	// The management port always stays on loopback. Direct hosted ingress
	// additionally binds the server-native HTTP/TLS listeners on 80/443.
	envArgs := hostedProcessEnv(spec)
	quarantineEnv := "APTEVA_CLONE_QUARANTINE=0"
	if spec.Quarantine {
		quarantineEnv = "APTEVA_CLONE_QUARANTINE=1"
	}
	inner := fmt.Sprintf(
		`exec env %s %s %s --data-dir %s --port %d --no-browser >>%s 2>&1`,
		envArgs, quarantineEnv, sh(binPath), sh(dataDir), spec.Port, sh(logPath),
	)
	spawnCmd := fmt.Sprintf(`
set -eu
DATA_DIR=%s
PID_FILE=%s
PORT=%d
if [ -f "$PID_FILE" ]; then
  OLD_PID=$(cat "$PID_FILE" 2>/dev/null || true)
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    if tr '\000' '\n' < "/proc/$OLD_PID/cmdline" 2>/dev/null | grep -Fxq -- "$DATA_DIR"; then
      echo "already running pid=$OLD_PID"; exit 0
    fi
    echo "refusing stale pid file owned by another process" >&2; exit 42
  fi
  rm -f "$PID_FILE"
fi
if ss -ltnH "sport = :$PORT" 2>/dev/null | grep -q . || lsof -i "tcp:$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "port $PORT is already in use" >&2
  exit 73
fi
setsid sh -c %s >/dev/null 2>&1 &
PID=$!
echo "$PID" > "$PID_FILE"
`, sh(dataDir), sh(pidPath), spec.Port, sh(inner))
	if out, code, err := instanceRunCommand(ctx, spec.InstanceID, spawnCmd, 10); err != nil || code != 0 {
		return "", "", fmt.Errorf("spawn remote apteva: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}

	baseURL = fmt.Sprintf("http://%s:%d", spec.InstanceIP, spec.Port)

	// 4) Wait for /api/health to respond.
	if err := a.waitForHostedReady(ctx, spec.InstanceID, spec.Port, 60*time.Second); err != nil {
		// Capture log tail to make the failure useful.
		tail, _, _ := instanceRunCommand(ctx, spec.InstanceID,
			fmt.Sprintf(`tail -50 %s 2>/dev/null || true`, sh(logPath)), 5)
		return "", baseURL, fmt.Errorf("hosted tenant did not become ready: %w; log tail:\n%s", err, tail)
	}

	// 5) Scrape setup_token from the log (first boot only). Mirror of
	//    localproc.go's scrapeSetupToken — same regex.
	if spec.FreshSetup {
		out, _, err := instanceRunCommand(ctx, spec.InstanceID,
			fmt.Sprintf(`tail -200 %s 2>/dev/null | grep -oE 'apt_[0-9a-f]{32}' | head -1`,
				sh(logPath)),
			10)
		if err == nil {
			setupToken = strings.TrimSpace(out)
		}
		if setupToken == "" {
			// Some apteva versions might race the log write — give
			// it one short retry.
			time.Sleep(2 * time.Second)
			out, _, _ = instanceRunCommand(ctx, spec.InstanceID,
				fmt.Sprintf(`tail -500 %s 2>/dev/null | grep -oE 'apt_[0-9a-f]{32}' | head -1`,
					sh(logPath)),
				10)
			setupToken = strings.TrimSpace(out)
		}
		if setupToken == "" {
			return "", baseURL, errors.New("setup token not found in remote apteva log; check log via instance_run_command")
		}
	}
	return setupToken, baseURL, nil
}

// stopHostedTenant SIGTERMs (then SIGKILLs) the apteva-server process
// listening on the tenant's port on the VPS. Mirrors fleet's local
// stopTenantBy logic — find pid by port via lsof or ss, kill the
// whole process group.
//
// `port` is the local-to-VPS port (what's bound on the VPS, not what
// fleet calls externally — they're the same number in v0.6).
func stopHostedTenant(ctx *sdk.AppCtx, instanceID int64, slug string, port int, grace time.Duration) error {
	if instanceID == 0 {
		return errors.New("stopHostedTenant: instance_id required")
	}
	if _, err := validatedTenantSlug(slug); err != nil {
		return err
	}
	if port == 0 {
		return nil
	}
	if err := validateTenantPort(port); err != nil {
		return err
	}
	// One round-trip: find pid+pgrp, kill -PGID, wait for port to
	// free, escalate to SIGKILL on timeout.
	graceSec := int(grace.Seconds())
	if graceSec <= 0 {
		graceSec = 10
	}
	dataDir := remoteFleetRoot + "/" + slug
	pidPath := dataDir + "/fleet.pid"
	script := fmt.Sprintf(`
set -u
PORT=%d
GRACE=%d
DATA_DIR=%s
PID_FILE=%s
PID=$(cat "$PID_FILE" 2>/dev/null || true)
if [ -z "$PID" ] || ! kill -0 "$PID" 2>/dev/null; then
  PID=$( (lsof -ti tcp:$PORT -sTCP:LISTEN 2>/dev/null || ss -ltnpH "sport = :$PORT" 2>/dev/null | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2) | head -1)
fi
if [ -z "$PID" ]; then rm -f "$PID_FILE"; echo "no listener"; exit 0; fi
case "$PID" in *[!0-9]*|'') echo "invalid tenant pid" >&2; exit 42;; esac
if ! tr '\000' '\n' < "/proc/$PID/cmdline" 2>/dev/null | grep -Fxq -- "$DATA_DIR"; then
  echo "refusing to stop pid=$PID: command has no exact $DATA_DIR argument" >&2
  exit 42
fi
PGID=$(ps -o pgid= -p $PID 2>/dev/null | tr -d ' ')
[ -z "$PGID" ] && PGID=$PID
case "$PGID" in *[!0-9]*|'') echo "invalid tenant process group" >&2; exit 42;; esac
if [ "$PGID" -le 1 ]; then echo "refusing unsafe process group $PGID" >&2; exit 42; fi
kill -TERM -$PGID 2>/dev/null
for i in $(seq 1 $GRACE); do
  sleep 1
  if ! ss -ltn "sport = :$PORT" 2>/dev/null | grep -q :$PORT && ! lsof -i tcp:$PORT -sTCP:LISTEN >/dev/null 2>&1; then
    rm -f "$PID_FILE"
    echo "stopped pid=$PID pgrp=$PGID"
    exit 0
  fi
done
kill -KILL -$PGID 2>/dev/null
kill -KILL $PID 2>/dev/null
sleep 1
if ss -ltnH "sport = :$PORT" 2>/dev/null | grep -q . || lsof -i "tcp:$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "tenant listener still active after SIGKILL" >&2
  exit 1
fi
rm -f "$PID_FILE"
echo "sigkilled pid=$PID pgrp=$PGID"
`, port, graceSec, sh(dataDir), sh(pidPath))
	out, code, err := instanceRunCommand(ctx, instanceID, script, graceSec+15)
	if err != nil || code != 0 {
		return fmt.Errorf("remote stop: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

// destroyHostedTenant wipes the tenant's remote data dir. Called from
// tenant_delete with confirm=true. Caller is responsible for stopping
// the process first.
func destroyHostedTenant(ctx *sdk.AppCtx, instanceID int64, slug string) error {
	if instanceID == 0 || slug == "" {
		return errors.New("destroyHostedTenant: instance_id and slug required")
	}
	valid, err := validatedTenantSlug(slug)
	if err != nil {
		return err
	}
	dataDir := remoteFleetRoot + "/" + valid
	// Refuse paths that don't sit under the fleet root — paranoia
	// against a slug containing path traversal (the slug validator
	// already rejects '/' but belt and suspenders).
	if strings.Contains(dataDir, "..") {
		return errors.New("refusing to rm a path containing ..")
	}
	if _, code, err := instanceRunCommand(ctx, instanceID,
		fmt.Sprintf(`rm -rf %s`, sh(dataDir)), 30); err != nil || code != 0 {
		return fmt.Errorf("remote rm: %w (exit %d)", err, code)
	}
	return nil
}

// waitForRemoteReady polls http://base/api/health until 200 or
// timeout. Uses fleet's existing httpClient (10s per-request).
func waitForRemoteReady(_ *sdk.AppCtx, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := baseURL + "/api/health"
	for {
		if time.Now().After(deadline) {
			return errors.New("timeout waiting for /api/health")
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func (a *App) waitForHostedReady(ctx *sdk.AppCtx, instanceID int64, port int, timeout time.Duration) error {
	baseURL, err := a.hostedTunnelBaseURL(ctx, instanceID, port)
	if err != nil {
		return fmt.Errorf("open hosted readiness tunnel: %w", err)
	}
	return waitForRemoteReady(ctx, baseURL, timeout)
}

func hostedPortListening(ctx *sdk.AppCtx, instanceID int64, port int) (bool, error) {
	if instanceID <= 0 {
		return false, errors.New("hosted port check requires instance_id")
	}
	if err := validateTenantPort(port); err != nil {
		return false, err
	}
	cmd := fmt.Sprintf(`PORT=%d; if ss -ltnH "sport = :$PORT" 2>/dev/null | grep -q . || lsof -i "tcp:$PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo listening; else echo stopped; fi`, port)
	out, code, err := instanceRunCommand(ctx, instanceID, cmd, 10)
	if err != nil || code != 0 {
		return false, fmt.Errorf("check hosted port: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	switch strings.TrimSpace(out) {
	case "listening":
		return true, nil
	case "stopped":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected hosted port check response %q", strings.TrimSpace(out))
	}
}

// pickHostedPort picks a port for a new hosted tenant. If the caller
// passed one, use it; else default to defaultHostedTenantPort + (number
// of existing tenants on this instance). Not race-free against a third
// party binding that port between now and spawn, but good enough for
// v0.6 (operator can pass an explicit port to dodge collisions).
func (a *App) pickHostedPort(ctx *sdk.AppCtx, instanceID int64, override int) (int, error) {
	rows, _ := a.store.list(map[string]string{"kind": KindLocal}) // includes hosted (kind=local) too
	used := map[int]bool{}
	for _, t := range rows {
		if t.InstanceID == instanceID {
			if port, _ := portFromBaseURL(t.BaseURL); port > 0 {
				used[port] = true
			}
		}
	}
	check := func(port int) (bool, error) {
		if err := validateTenantPort(port); err != nil {
			return false, err
		}
		if used[port] {
			return false, nil
		}
		cmd := fmt.Sprintf(`PORT=%d; if ss -ltnH "sport = :$PORT" 2>/dev/null | grep -q . || lsof -i "tcp:$PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo used; else echo free; fi`, port)
		out, _, err := instanceRunCommand(ctx, instanceID, cmd, 10)
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(out) == "free", nil
	}
	if override > 0 {
		free, err := check(override)
		if err != nil {
			return 0, err
		}
		if !free {
			return 0, fmt.Errorf("hosted port %d is already reserved or in use", override)
		}
		return override, nil
	}
	for port := defaultHostedTenantPort; port <= 7999; port++ {
		free, err := check(port)
		if err != nil {
			return 0, err
		}
		if free {
			return port, nil
		}
	}
	return 0, errors.New("no free hosted tenant port in range 7100-7999")
}

// sh wraps an argument in single quotes for safe shell interpolation.
// Apostrophes get the standard `'\”` dance. Used everywhere we build
// commands for instance_run_command — keeps slugs / paths / versions
// from being able to inject shell metacharacters.
func sh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ─── Hosted toolCreate ─────────────────────────────────────────────
//
// toolCreate (handlers.go) early-dispatches here when args["instance_id"]
// resolves > 0. Shape mirrors the local create path so the
// auto-setup-or-setup_pending response is the same; only spawn,
// stop, and the base_url differ.

func (a *App) toolCreateHosted(ctx *sdk.AppCtx, args map[string]any, slug, owner string, instanceID int64, pendingTemplate *pendingTenantTemplate) (any, error) {
	if _, err := validatedTenantSlug(slug); err != nil {
		return nil, err
	}
	if _, _, err := a.store.getBySlug(slug); err == nil {
		return nil, fmt.Errorf("slug %q already in use", slug)
	}
	info, err := a.getInstanceInfo(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// Resolve the apteva version: explicit arg → npm latest → "latest"
	// string (the remote npm install will resolve). Same precedence
	// the local path uses via resolveSpawnBin.
	version := strings.TrimSpace(getStr(args, "apteva_version"))
	if version == "" {
		if env := strings.TrimSpace(os.Getenv("FLEET_DEFAULT_APTEVA_VERSION")); env != "" {
			version = env
		} else {
			version = "latest"
		}
	}
	if version == "latest" {
		if v, lerr := npmLatestVersion(context.Background()); lerr == nil {
			version = v
		}
	}
	version, err = validateAptevaVersion(version, false)
	if err != nil {
		return nil, err
	}

	portOverride := intArg(args, "port", 0)
	port, err := a.pickHostedPort(ctx, instanceID, portOverride)
	if err != nil {
		return nil, err
	}

	t := &Tenant{
		Slug: slug,
		// kind=local because fleet supervises the lifecycle (vs
		// kind=remote which means "fleet only registers an existing
		// apteva-server"). InstanceID is the discriminator for
		// where the process actually runs. Avoiding a new kind value
		// keeps the CHECK constraint migration-free.
		Kind:          KindLocal,
		BaseURL:       fmt.Sprintf("http://%s:%d", info.PublicIPv4, port),
		ConfigDir:     remoteFleetRoot + "/" + slug,
		OwnerEmail:    owner,
		Status:        StatusStarting,
		InstanceID:    instanceID,
		TargetVersion: version,
		IngressMode:   IngressParent,
	}
	apiKeyStub, err := a.keys.seal([]byte("pending"))
	if err != nil {
		return nil, err
	}
	if err := a.store.insert(t, apiKeyStub, nil); err != nil {
		return nil, err
	}
	if pendingTemplate != nil {
		pendingTemplate.TenantID = t.ID
		if err := a.store.savePendingTemplate(*pendingTemplate); err != nil {
			_ = a.store.hardDelete(t.ID)
			return nil, fmt.Errorf("save requested template: %w", err)
		}
	}
	_ = a.store.recordEvent(t.ID, "spawn_start", "user",
		map[string]any{"instance_id": instanceID, "instance_ip": info.PublicIPv4, "port": port})

	setupToken, baseURL, spawnErr := a.spawnHostedTenant(ctx, hostedSpawnSpec{
		InstanceID: instanceID,
		InstanceIP: info.PublicIPv4,
		Slug:       slug,
		Port:       port,
		AptevaVer:  version,
		FreshSetup: true,
	})
	if spawnErr != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		_ = a.store.recordEvent(t.ID, "spawn_failed", "user",
			map[string]any{"error": spawnErr.Error()})
		// Leave the data dir + any rows on the VPS in place — operator
		// can use tenant_delete with confirm=true to wipe, or
		// re-run create after fixing whatever broke.
		return nil, spawnErr
	}
	_ = a.store.recordEvent(t.ID, "spawned", "user",
		map[string]any{"base_url": baseURL, "instance_id": instanceID, "port": port})

	controlURL, err := a.hostedTunnelBaseURL(ctx, instanceID, port)
	if err != nil {
		return nil, fmt.Errorf("open hosted setup tunnel: %w", err)
	}
	// Setup credentials stay inside the SSH tunnel; baseURL is only
	// retained as the tenant's operator-facing location.
	autoSetup, err := a.autoSetupTenant(context.Background(), controlURL, setupToken, owner, "")
	if err != nil {
		ctx.Logger().Warn("hosted: auto-setup failed, falling back to setup_pending",
			"tenant", t.ID, "err", err)
		setupTokenEnc, sealErr := a.keys.seal([]byte(setupToken))
		if sealErr != nil {
			return nil, sealErr
		}
		if _, dbErr := a.store.db.Exec(
			`UPDATE fleet_tenants SET setup_token_enc = ?, status = ?, updated_at = ? WHERE id = ?`,
			setupTokenEnc, StatusSetupPending, time.Now().UTC(), t.ID,
		); dbErr != nil {
			return nil, dbErr
		}
		_ = a.store.recordEvent(t.ID, "auto_setup_failed", "user",
			map[string]any{"error": err.Error()})
		out := map[string]any{
			"tenant_id":        t.ID,
			"slug":             slug,
			"base_url":         baseURL,
			"status":           StatusSetupPending,
			"setup_url":        baseURL + "/?setup=1",
			"setup_token":      setupToken,
			"instance_id":      instanceID,
			"auto_setup_error": err.Error(),
		}
		if pendingTemplate != nil {
			out["template"] = pendingTemplateStatus(*pendingTemplate)
		}
		return out, nil
	}

	// Auto-setup happy path — seal the api_key, flip to active.
	enc, err := a.keys.seal([]byte(autoSetup.APIKey))
	if err != nil {
		return nil, fmt.Errorf("seal api_key: %w", err)
	}
	if err := a.store.attachAPIKey(t.ID, enc); err != nil {
		return nil, fmt.Errorf("attach api_key: %w", err)
	}
	_ = a.store.recordEvent(t.ID, "auto_setup_complete", "user",
		map[string]any{"admin_email": owner})
	out := map[string]any{
		"tenant_id":      t.ID,
		"slug":           slug,
		"base_url":       baseURL,
		"status":         StatusActive,
		"admin_email":    owner,
		"admin_password": autoSetup.Password,
		"api_key":        autoSetup.APIKey,
		"instance_id":    instanceID,
		"target_version": version,
	}
	if pendingTemplate != nil {
		out["template"] = a.applyPendingTemplateBestEffort(ctx, t, autoSetup.APIKey)
	}
	out["a2a"] = a.reconcileTenantA2ABestEffort(ctx, t.ID, "tool:tenant_create")
	return out, nil
}
