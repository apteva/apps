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
//	  versions/<v>/node_modules/apteva/*       # pinned CLI/server/core binaries
//	  <slug>/                                  # tenant data dir
//	    apteva.db, apps/, fleet-child.log, ...
//
// Port allocation: fleet doesn't ask the VPS for a free port (would
// race; SSH round-trip too slow per-create). Operator picks the port
// at tenant_create time (default 7100 + tenant-count-on-instance), or
// passes one explicitly. v0.6.0 ships single-tenant-per-VPS as the
// happy path; multi-tenant packing is v0.7.
//
// The apteva-server runs under `setsid` so it detaches from the SSH session
// and survives the connection drop. Operator-driven stop revalidates and
// signals every tenant-owned process in that session; child components use
// separate process groups, so the fleet.pid PGID alone is not sufficient.

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func hostedVersionInstallScript(versionDir, version string) string {
	runtime := versionedRuntimePaths(versionDir)
	return fmt.Sprintf(`set -eu
VERSION=%s
	VERSION_DIR=%s
	CLI=%s
	SERVER=%s
	CORE=%s
	PACKAGE_JSON="$VERSION_DIR/node_modules/apteva/package.json"
 FINAL_DIR="$VERSION_DIR"
 mkdir -p "$(dirname "$VERSION_DIR")"
 command -v flock >/dev/null 2>&1 || { echo "flock required for atomic runtime installation" >&2; exit 127; }
 exec 9>"$VERSION_DIR.install.lock"
 flock -w 240 9
	if [ ! -x "$CLI" ] || [ ! -x "$SERVER" ] || [ ! -x "$CORE" ]; then
  VERSION_DIR=$(mktemp -d "$FINAL_DIR.stage-XXXXXX")
  CLI="$VERSION_DIR/node_modules/apteva/apteva"
  SERVER="$VERSION_DIR/node_modules/apteva/apteva-server"
  CORE="$VERSION_DIR/node_modules/apteva/apteva-core"
  PACKAGE_JSON="$VERSION_DIR/node_modules/apteva/package.json"
  INSTALL_HOME="$VERSION_DIR/.npm-apteva-home"
  rm -rf -- "$INSTALL_HOME"
  mkdir -p "$INSTALL_HOME"
  trap 'rm -rf -- "$INSTALL_HOME"; if [ "$VERSION_DIR" != "$FINAL_DIR" ]; then rm -rf -- "$VERSION_DIR"; fi' EXIT
  env APTEVA_HOME="$INSTALL_HOME" npm install --prefix "$VERSION_DIR" --no-audit --no-fund --silent "apteva@$VERSION"
fi
	[ -f "$PACKAGE_JSON" ] || { echo "versioned package manifest missing: $PACKAGE_JSON" >&2; exit 65; }
	package_version=$(node -e 'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(String(p.version||""))' "$PACKAGE_JSON")
	package_version=${package_version#v}
	expected="$VERSION"
	[ "$expected" = latest ] && expected="$package_version"
	expected=${expected#v}
	[ -n "$package_version" ] && [ "$package_version" = "$expected" ] || { echo "Requested Apteva $expected, but package manifest reports ${package_version:-unknown}" >&2; exit 65; }
	for item in "apteva:$CLI" "apteva-server:$SERVER" "apteva-core:$CORE"; do
	  label=${item%%%%:*}
	  bin=${item#*:}
	  [ -x "$bin" ] || { echo "versioned $label binary missing: $bin" >&2; exit 65; }
	done
	# apteva-core has no side-effect-free --version command. Its package
	# manifest and executable path are validated above; do not boot it while
	# preparing a stopped tenant.
	for item in "apteva:$CLI" "apteva-server:$SERVER"; do
	  label=${item%%%%:*}
	  bin=${item#*:}
	  actual=$("$bin" --version 2>&1 | awk 'NF >= 2 { print $2; exit }')
	  actual=${actual#v}
	  [ "$actual" = "$expected" ] || { echo "Requested Apteva $expected, but $label reports ${actual:-unknown}" >&2; exit 65; }
done
if [ "$VERSION_DIR" != "$FINAL_DIR" ]; then
  rm -rf -- "$INSTALL_HOME"
  if [ -e "$FINAL_DIR" ]; then mv "$FINAL_DIR" "$FINAL_DIR.invalid-$(date +%%s)-$$"; fi
  mv "$VERSION_DIR" "$FINAL_DIR"
fi
printf 'FLEET_VERSION_READY %%s\n' "$expected"`, sh(version), sh(versionDir), sh(runtime.CLI), sh(runtime.Server), sh(runtime.Core))
}

func (a *App) ensureHostedVersionInstalled(ctx *sdk.AppCtx, instanceID int64, version string) (aptevaRuntimePaths, string, error) {
	version, err := validateAptevaVersion(version, false)
	if err != nil || version == "" {
		if err == nil {
			err = errors.New("version required")
		}
		return aptevaRuntimePaths{}, "", err
	}
	if err := a.ensureHostedRuntime(ctx, instanceID); err != nil {
		return aptevaRuntimePaths{}, "", err
	}
	unlock, lockErr := lockResource(context.Background(), fmt.Sprintf("host-version:%d:%s", instanceID, version))
	if lockErr != nil {
		return aptevaRuntimePaths{}, "", lockErr
	}
	defer unlock()
	versionDir := remoteFleetRoot + "/versions/" + version
	runtime := versionedRuntimePaths(versionDir)
	out, code, err := instanceRunCommand(ctx, instanceID, hostedVersionInstallScript(versionDir, version), 240)
	if err != nil || code != 0 {
		return aptevaRuntimePaths{}, "", fmt.Errorf("prepare apteva@%s runtime: %w (exit %d): %s", version, err, code, strings.TrimSpace(out))
	}
	const marker = "FLEET_VERSION_READY "
	actual := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			actual = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), marker))
		}
	}
	if actual == "" {
		return aptevaRuntimePaths{}, "", errors.New("hosted version preparation returned no readiness marker")
	}
	return runtime, actual, nil
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
	logPath := dataDir + "/fleet-child.log"
	pidPath := dataDir + "/fleet.pid"

	// A fresh provisioning attempt must never adopt an existing directory.
	mkdirCmd := fmt.Sprintf(`mkdir -p %s`, sh(dataDir))
	if spec.FreshSetup {
		mkdirCmd = fmt.Sprintf(`mkdir -p %s && { mkdir %s || exit 73; }`, sh(remoteFleetRoot), sh(dataDir))
	}
	if _, code, err := instanceRunCommand(ctx, spec.InstanceID, mkdirCmd, 30); err != nil || code != 0 {
		if code == 73 {
			return "", "", errHostedDataDirExists
		}
		return "", "", fmt.Errorf("mkdir remote dirs: %w (exit %d)", err, code)
	}
	// 2) Prepare and validate the exact package binaries. npm's .bin/apteva
	// launcher is deliberately bypassed because it prefers ~/.apteva/bin.
	runtime, expectedVersion, err := a.ensureHostedVersionInstalled(ctx, spec.InstanceID, spec.AptevaVer)
	if err != nil {
		return "", "", err
	}

	// 3) Spawn under setsid so the process survives the SSH session drop.
	// The management port always stays on loopback. Direct hosted ingress
	// additionally binds the server-native HTTP/TLS listeners on 80/443.
	envArgs := hostedProcessEnv(spec)
	if tenant, _, lookupErr := a.store.getBySlug(spec.Slug); lookupErr == nil {
		dnsEnv, envErr := a.tenantDNSEnv(tenant.ID, true)
		if envErr != nil {
			return "", "", envErr
		}
		base, envErr := a.reserveAppPortBlock(tenant.ID, spec.InstanceID, spec.Port)
		if envErr != nil {
			return "", "", envErr
		}
		dnsEnv = applyAppPortEnv(dnsEnv, base)
		for _, kv := range dnsEnv {
			envArgs += " " + sh(kv)
		}
	}

	quarantineEnv := "APTEVA_CLONE_QUARANTINE=0"
	if spec.Quarantine {
		quarantineEnv = "APTEVA_CLONE_QUARANTINE=1"
	}
	runtimeEnv := fmt.Sprintf("APTEVA_HOME=%s APTEVA_SERVER_BIN=%s APTEVA_CORE_BIN=%s", sh(dataDir), sh(runtime.Server), sh(runtime.Core))
	inner := fmt.Sprintf(
		`exec env %s %s %s %s --data-dir %s --port %d --no-browser >>%s 2>&1`,
		envArgs, quarantineEnv, runtimeEnv, sh(runtime.CLI), sh(dataDir), spec.Port, sh(logPath),
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
SID=$(ps -o sid= -p "$PID" 2>/dev/null | tr -d ' ')
case "$SID" in ''|*[!0-9]*) rm -f "$PID_FILE"; kill -TERM "$PID" 2>/dev/null || true; echo "failed to record tenant session" >&2; exit 70;; esac
echo "$SID" > "$DATA_DIR/fleet.sid"
`, sh(dataDir), sh(pidPath), spec.Port, sh(inner))
	if out, code, err := instanceRunCommand(ctx, spec.InstanceID, spawnCmd, 10); err != nil || code != 0 {
		return "", "", fmt.Errorf("spawn remote apteva: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}

	baseURL = fmt.Sprintf("http://%s:%d", spec.InstanceIP, spec.Port)

	// 4) Wait for /api/health to respond.
	if err := a.waitForHostedReadyVersion(ctx, spec.InstanceID, spec.Port, expectedVersion, 60*time.Second); err != nil {
		_ = stopHostedTenant(ctx, spec.InstanceID, spec.Slug, spec.Port, 10*time.Second)
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

var errHostedStopIndeterminate = errors.New("hosted tenant stop state is indeterminate")

// hostedProcessControlScript identifies a legacy hosted tenant by its exact
// APTEVA_HOME/data-dir marker, including residual workers from older sessions.
// The recorded session validates the launcher/listener, but does not bound
// the ownership scan. Individual processes are revalidated before signalling;
// SSH control ancestors and independent static deployment groups are excluded.
func hostedProcessControlScript(dataDir string, port, graceSec int, stop bool) string {
	action := "inspect"
	if stop {
		action = "stop"
	}
	return fmt.Sprintf(`set -u
DATA_DIR=%s
PORT=%d
GRACE=%d
ACTION=%s
PID_FILE="$DATA_DIR/fleet.pid"
SID_FILE="$DATA_DIR/fleet.sid"
CONTROL_PIDS=" $$ "
control_parent=$PPID
while [ "${control_parent:-0}" -gt 1 ] 2>/dev/null; do
  CONTROL_PIDS="$CONTROL_PIDS $control_parent "
  control_parent=$(ps -o ppid= -p "$control_parent" 2>/dev/null | tr -d ' ')
done
numeric() { case "${1:-}" in ''|*[!0-9]*) return 1;; *) return 0;; esac; }
proc_sid() { ps -o sid= -p "$1" 2>/dev/null | tr -d ' '; }
proc_pgid() { ps -o pgid= -p "$1" 2>/dev/null | tr -d ' '; }
proc_live() {
  [ -d "/proc/$1" ] || return 1
  state=$(sed 's/^.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f1)
  [ -n "$state" ] && [ "$state" != Z ]
}
is_static() { (tr '\000' '\n' < "/proc/$1/cmdline") 2>/dev/null | grep -Fxq -- '--static-server'; }
is_static_group() {
  pgid=$(proc_pgid "$1")
  numeric "$pgid" || return 1
  case " $STATIC_PGIDS " in *" $pgid "*) return 0;; esac
  return 1
}
is_owned() {
  pid="$1"
  case "$CONTROL_PIDS" in *" $pid "*) return 1;; esac
  proc_live "$pid" || return 1
  if (tr '\000' '\n' < "/proc/$pid/environ") 2>/dev/null | grep -Fxq -- "APTEVA_HOME=$DATA_DIR"; then return 0; fi
  if (tr '\000' '\n' < "/proc/$pid/cmdline") 2>/dev/null | grep -Fxq -- "$DATA_DIR"; then return 0; fi
  exe=$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)
  case "$exe" in "$DATA_DIR"/*) return 0;; esac
  # Older cores may lack APTEVA_HOME and belong to a previous session.
  # Require BOTH the managed core binary and an exact instance directory.
  case "$exe" in /var/lib/apteva-fleet/versions/*/apteva-core)
    cwd=$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)
    case "$cwd" in "$DATA_DIR"/instance_*)
      instance_number=${cwd#"$DATA_DIR"/instance_}
      numeric "$instance_number" && return 0;;
    esac;;
  esac
  return 1
}
listener_pids() {
  (lsof -ti "tcp:$PORT" -sTCP:LISTEN 2>/dev/null || true
   ss -ltnpH "sport = :$PORT" 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 || true) | sort -un
}
refresh() {
  ROOT=$(cat "$PID_FILE" 2>/dev/null || true)
  numeric "$ROOT" || ROOT=''
  SID=$(cat "$SID_FILE" 2>/dev/null || true)
  numeric "$SID" || SID=''
  LISTENERS=$(listener_pids)
  UNKNOWN=''
  if [ -n "$ROOT" ] && proc_live "$ROOT"; then
    if ! is_owned "$ROOT" || is_static "$ROOT"; then UNKNOWN="pid-file process $ROOT is not tenant-owned"
    else
      root_sid=$(proc_sid "$ROOT")
      if ! numeric "$root_sid" || [ "$root_sid" -le 1 ]; then UNKNOWN="unsafe root session ${root_sid:-unknown}"
      elif [ -n "$SID" ] && [ "$SID" != "$root_sid" ]; then UNKNOWN="recorded session $SID differs from root session $root_sid"
      else SID="$root_sid"; fi
    fi
  fi
  for pid in $LISTENERS; do
    if ! numeric "$pid" || ! is_owned "$pid" || is_static "$pid"; then UNKNOWN="listener ${pid:-unknown} is not tenant-owned"; continue; fi
    listener_sid=$(proc_sid "$pid")
    if ! numeric "$listener_sid" || [ "$listener_sid" -le 1 ]; then UNKNOWN="unsafe listener session ${listener_sid:-unknown}"
    elif [ -n "$SID" ] && [ "$SID" != "$listener_sid" ]; then UNKNOWN="listener session $listener_sid differs from tenant session $SID"
    else SID="$listener_sid"; fi
  done
  STATIC_PGIDS=''
  for proc in /proc/[0-9]*; do
      pid=${proc#/proc/}
      proc_live "$pid" || continue
      is_static "$pid" || continue
      pgid=$(proc_pgid "$pid")
      numeric "$pgid" && STATIC_PGIDS="$STATIC_PGIDS $pgid"
  done
  OWNED=''
  for proc in /proc/[0-9]*; do
      pid=${proc#/proc/}
      proc_live "$pid" || continue
      is_owned "$pid" || continue
      is_static_group "$pid" && continue
      OWNED="$OWNED $pid"
  done
}
signal_owned() {
  # Revalidate each candidate immediately before signalling, and protect
  # the SSH control ancestry and independently managed static deployments.
  for candidate in $OWNED; do
    is_owned "$candidate" || continue
    is_static "$candidate" && continue
    is_static_group "$candidate" && continue
    kill -"$1" "$candidate" 2>/dev/null || true
  done
}
emit_state() {
  refresh
  if [ -n "$UNKNOWN" ]; then state=unknown
  elif [ -z "$LISTENERS" ] && [ -z "${OWNED# }" ]; then state=stopped
  else state=running
  fi
  printf 'FLEET_STOP_STATE %%s root=%%s sid=%%s listeners=%%s owned=%%s reason=%%s\n' "$state" "${ROOT:-none}" "${SID:-none}" "${LISTENERS:-none}" "${OWNED# }" "${UNKNOWN:-none}"
  [ "$state" = stopped ] && return 0
  [ "$state" = running ] && return 3
  return 4
}
refresh
if [ "$ACTION" = inspect ]; then emit_state; exit $?; fi
if [ -n "$UNKNOWN" ]; then emit_state; exit $?; fi
if [ -z "$LISTENERS" ] && [ -z "${OWNED# }" ]; then rm -f "$PID_FILE" "$SID_FILE"; emit_state; exit 0; fi
if numeric "$SID" && [ "$SID" -gt 1 ]; then printf '%%s\n' "$SID" > "$SID_FILE"; fi
signal_owned TERM
for i in $(seq 1 "$GRACE"); do
  sleep 1
  refresh
  if [ -n "$UNKNOWN" ]; then emit_state; exit $?; fi
  if [ -z "$LISTENERS" ] && [ -z "${OWNED# }" ]; then rm -f "$PID_FILE" "$SID_FILE"; emit_state; exit 0; fi
  signal_owned TERM
done
refresh
if [ -n "$UNKNOWN" ]; then emit_state; exit $?; fi
signal_owned KILL
sleep 1
refresh
if [ -z "$UNKNOWN" ] && [ -z "$LISTENERS" ] && [ -z "${OWNED# }" ]; then rm -f "$PID_FILE" "$SID_FILE"; emit_state; exit 0; fi
emit_state
exit $?`, sh(dataDir), port, graceSec, sh(action))
}

func hostedStopState(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "FLEET_STOP_STATE" {
			return fields[1]
		}
	}
	return ""
}

// stopHostedTenant stops every validated process in the tenant's session and
// then verifies the postcondition. If SSH completion is ambiguous, a separate
// command re-inspects the listener and owned processes before deciding whether
// the stop succeeded.
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
	graceSec := int(grace.Seconds())
	if graceSec <= 0 {
		graceSec = 10
	}
	dataDir := remoteFleetRoot + "/" + slug
	script := hostedProcessControlScript(dataDir, port, graceSec, true)
	out, code, err := instanceRunCommand(ctx, instanceID, script, graceSec+15)
	if err == nil && code == 0 && hostedStopState(out) == "stopped" {
		return nil
	}
	firstDetail := fmt.Sprintf("remote stop exit %d: %s", code, strings.TrimSpace(out))
	if err != nil {
		firstDetail = fmt.Sprintf("remote stop: %v (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	inspect := hostedProcessControlScript(dataDir, port, 1, false)
	var checkOut string
	var checkCode int
	var checkErr error
	// Instances >=0.4.44 owns a fresh administrative transport for each call.
	// Only re-read state after a transient transport failure. Never replay
	// the stop mutation, and never override an explicit ownership ambiguity.
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
		}
		checkOut, checkCode, checkErr = instanceRunCommand(ctx, instanceID, inspect, 15)
		state := hostedStopState(checkOut)
		if state == "stopped" && checkErr == nil && checkCode == 0 {
			return nil
		}
		if state == "running" && checkCode == 3 {
			return fmt.Errorf("%s; verified tenant is still running (exit %d): %s", firstDetail, checkCode, strings.TrimSpace(checkOut))
		}
		if state != "" || checkCode >= 0 {
			break
		}
	}
	return fmt.Errorf("%w: %s; follow-up inspection failed (exit %d): %s", errHostedStopIndeterminate, firstDetail, checkCode, strings.TrimSpace(checkOut)+" "+errorString(checkErr))
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
func waitForRemoteReadyVersion(_ *sdk.AppCtx, baseURL, expectedVersion string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := baseURL + "/api/health"
	for {
		if time.Now().After(deadline) {
			return errors.New("timeout waiting for /api/health")
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				actual := healthVersion(body)
				if expectedVersion != "" && actual != strings.TrimPrefix(expectedVersion, "v") {
					if actual == "" {
						actual = "unknown"
					}
					return fmt.Errorf("requested Apteva %s, but launched runtime reports %s", expectedVersion, actual)
				}
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForRemoteReady(ctx *sdk.AppCtx, baseURL string, timeout time.Duration) error {
	return waitForRemoteReadyVersion(ctx, baseURL, "", timeout)
}

func (a *App) waitForHostedReady(ctx *sdk.AppCtx, instanceID int64, port int, timeout time.Duration) error {
	return a.waitForHostedReadyVersion(ctx, instanceID, port, "", timeout)
}

func (a *App) waitForHostedReadyVersion(ctx *sdk.AppCtx, instanceID int64, port int, expectedVersion string, timeout time.Duration) error {
	baseURL, err := a.hostedTunnelBaseURL(ctx, instanceID, port)
	if err != nil {
		return fmt.Errorf("open hosted readiness tunnel: %w", err)
	}
	return waitForRemoteReadyVersion(ctx, baseURL, expectedVersion, timeout)
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
	rows, listErr := a.store.list(map[string]string{"kind": KindLocal}) // includes hosted (kind=local) too
	if listErr != nil {
		return 0, listErr
	}
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
		var reservations int
		if err := a.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM fleet_port_reservations WHERE instance_id=? AND port=?)+(SELECT COUNT(*) FROM fleet_app_port_blocks WHERE instance_id=? AND ? BETWEEN base AND base+999)`, instanceID, port, instanceID, port).Scan(&reservations); err != nil {
			return false, err
		}
		if reservations > 0 {
			return false, nil
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
	initializationDone, err := a.insertTenantForOperation(t, apiKeyStub, nil, "provision", false)
	if err != nil {
		return nil, err
	}
	defer initializationDone()
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
		if errors.Is(spawnErr, errHostedDataDirExists) {
			_ = a.store.hardDelete(t.ID)
			return nil, spawnErr
		}
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

	if err := a.persistSetupToken(t.ID, setupToken); err != nil {
		a.requireRecovery(t.ID, err)
		return nil, err
	}
	controlURL, err := a.hostedTunnelBaseURL(ctx, instanceID, port)
	if err != nil {
		return nil, fmt.Errorf("open hosted setup tunnel: %w", err)
	}
	// Setup credentials stay inside the SSH tunnel; baseURL is only
	// retained as the tenant's operator-facing location.
	autoSetup, err := a.provisionTenant(context.Background(), t, controlURL, setupToken, owner)
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

var errHostedDataDirExists = errors.New("remote tenant directory already exists or cannot be reserved; existing data was preserved")
