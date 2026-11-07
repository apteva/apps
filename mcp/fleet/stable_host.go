package main

// Fleet speaks the public host control protocol; it does not import server
// internals or edit the host journal. The host owns database compatibility,
// controller handoff and recovery against the current database.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const hostStartupTimeout = 4 * time.Minute
const hostDeploymentTimeout = 15 * time.Minute
const tenantLifecycleTimeout = hostStartupTimeout + hostDeploymentTimeout

// Keep a tenant-marked parent for the existing scope/process-tree stop logic.
// Running the host directly avoids the CLI's independent 15-second readiness
// deadline. The wrapper forwards a clean stop and remains until the host exits.
const stableHostLaunchScript = `
"$2" &
host_pid=$!
trap 'kill -TERM "$host_pid" 2>/dev/null || true; wait "$host_pid"; exit 0' TERM INT
wait "$host_pid"
exit $?
`

func stableHostLaunchEnv(env []string, s fleetHostState) []string {
	bind, port, _ := net.SplitHostPort(s.ListenAddr)
	env = setEnv(env, "APTEVA_BIND", bind)
	env = setEnv(env, "PORT", port)
	env = setEnv(env, "DB_PATH", s.DBPath)
	env = setEnv(env, "DATA_DIR", s.Home)
	env = setEnv(env, "APTEVA_SERVER_BIN", s.Binary)
	env = unsetEnv(env, "APTEVA_CORE_BIN")
	// The host resolves this sentinel to each candidate's sibling Core,
	// instead of retaining the bootstrap CLI's version forever.
	env = setEnv(env, "CORE_CMD", filepath.Join(s.Home, "bin", "apteva-core"))
	return env
}

type fleetHostJob struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Previous string `json:"previous"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
}

type fleetHostState struct {
	Protocol   int           `json:"protocol"`
	Home       string        `json:"home"`
	DBPath     string        `json:"db_path"`
	ListenAddr string        `json:"listen_addr"`
	PID        int           `json:"pid"`
	HostBinary string        `json:"host_binary"`
	Binary     string        `json:"binary"`
	Version    string        `json:"version"`
	Job        *fleetHostJob `json:"job,omitempty"`
	ActiveSlot *struct {
		PID       int `json:"pid"`
		Candidate struct {
			Version string `json:"version"`
			Binary  string `json:"binary"`
		} `json:"candidate"`
	} `json:"active_slot,omitempty"`
}

func hostTerminal(phase string) bool {
	return phase == "succeeded" || phase == "failed" || phase == "rolled_back" || phase == "recovery_required"
}

func validateFleetHost(s fleetHostState, t *Tenant) error {
	home := filepath.Clean(t.ConfigDir)
	_, port, err := net.SplitHostPort(s.ListenAddr)
	if err != nil || port != strconv.Itoa(portFromTenant(t)) || s.Protocol != 1 || s.Home != home || s.DBPath != filepath.Join(home, "apteva.db") || s.PID < 1 || !filepath.IsAbs(s.Binary) || !filepath.IsAbs(s.HostBinary) {
		return errors.New("stable host identity does not match tenant home, database, port or protocol; recovery required")
	}
	return nil
}

// Each remote request is short. Polling never holds an SSH command open for a
// whole deployment, nor exposes the private socket on the network.
const remoteHostRequest = `
import http.client, json, os, socket, sys
home, method, path, payload = sys.argv[1:]
sock = os.path.join(home, "host", "control.sock")
if method == "JOURNAL":
    state = os.path.join(home, "host", "state.json")
    if not os.path.lexists(state):
        if os.path.lexists(sock): raise RuntimeError("host socket exists without a journal")
        print('{"present":false}')
        sys.exit(0)
    with open(state) as f: data = json.load(f)
else:
    if not os.path.lexists(sock) and not os.path.lexists(os.path.join(home, "host", "state.json")):
        print('{"present":false}')
        sys.exit(0)
    class UnixHTTP(http.client.HTTPConnection):
        def connect(self):
            self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.sock.settimeout(5)
            self.sock.connect(sock)
    conn = UnixHTTP("host", timeout=5)
    conn.request(method, path, payload if payload != "null" else None, {"Content-Type":"application/json"})
    resp = conn.getresponse()
    body = resp.read(1048577)
    if len(body) > 1048576: raise RuntimeError("oversized host response")
    if resp.status < 200 or resp.status >= 300: raise RuntimeError("host HTTP %d: %s" % (resp.status, body[:4096].decode(errors="replace")))
    data = json.loads(body)
# Journal proxy keys are never returned to Fleet or the Instances tool logs.
for slot in ("active_slot", "pending_slot"):
    if isinstance(data.get(slot), dict): data[slot].pop("proxy_key", None)
print(json.dumps({"present":True, "state":data}))
`

func (a *App) tenantCommand(ctx context.Context, app *sdk.AppCtx, t *Tenant, command string, timeout time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.IsHosted() {
		out, code, err := instanceRunCommand(app, t.InstanceID, command, int(timeout.Seconds()))
		if err != nil || code != 0 {
			return nil, fmt.Errorf("tenant command (exit %d): %v: %s", code, err, strings.TrimSpace(out))
		}
		return []byte(out), nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = tenantSpawnEnv(t.ConfigDir, portFromTenant(t), t.ID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tenant command: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (a *App) hostRequest(ctx context.Context, app *sdk.AppCtx, t *Tenant, method, path string, input any) (fleetHostState, bool, error) {
	var s fleetHostState
	if t.IsHosted() {
		payload, err := json.Marshal(input)
		if err != nil {
			return s, true, err
		}
		command := "python3 -c " + sh(remoteHostRequest) + " " + sh(t.ConfigDir) + " " + sh(method) + " " + sh(path) + " " + sh(string(payload))
		out, err := a.tenantCommand(ctx, app, t, command, 10*time.Second)
		if err != nil {
			return s, true, err
		}
		var result struct {
			Present bool           `json:"present"`
			State   fleetHostState `json:"state"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			return s, true, fmt.Errorf("decode host response: %w", err)
		}
		if !result.Present {
			return s, false, nil
		}
		s = result.State
	} else {
		socketPath := filepath.Join(t.ConfigDir, "host", "control.sock")
		journalPath := filepath.Join(t.ConfigDir, "host", "state.json")
		_, se := os.Lstat(socketPath)
		_, je := os.Lstat(journalPath)
		if os.IsNotExist(se) && os.IsNotExist(je) {
			return s, false, nil
		}
		if (se != nil && !os.IsNotExist(se)) || (je != nil && !os.IsNotExist(je)) {
			return s, true, errors.Join(se, je)
		}
		if method == "JOURNAL" {
			b, err := os.ReadFile(journalPath)
			if err != nil {
				return s, true, err
			}
			if err = json.Unmarshal(b, &s); err != nil {
				return s, true, err
			}
		} else {
			payload, err := json.Marshal(input)
			if err != nil {
				return s, true, err
			}
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			}}}
			defer client.CloseIdleConnections()
			req, err := http.NewRequestWithContext(ctx, method, "http://host"+path, bytes.NewReader(payload))
			if err != nil {
				return s, true, err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return s, true, err
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				return s, true, fmt.Errorf("host HTTP %d: %s", resp.StatusCode, b)
			}
			if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil {
				return s, true, err
			}
		}
	}
	return s, true, validateFleetHost(s, t)
}

// Publish a complete release once. An existing immutable release is compared,
// never overwritten, even if it is the currently running controller.
const stageHostReleaseScript = `
import hashlib, os, shutil, sys, tempfile
source, home, version = sys.argv[1:]
root = os.path.join(home, "versions")
os.makedirs(root, mode=0o700, exist_ok=True)
if os.path.realpath(root) != root: raise RuntimeError("symlinked versions directory")
target = os.path.join(root, version)
def digest(path):
    result = hashlib.sha256()
    with open(path,"rb") as f:
        for block in iter(lambda: f.read(1048576), b""): result.update(block)
    return result.hexdigest()
def verify():
    if os.path.islink(target) or not os.path.isdir(target): raise RuntimeError("invalid release directory")
    for name in ("apteva", "apteva-server", "apteva-core", "apteva-host", "package.json"):
        src = os.path.join(source,name)
        dst = os.path.join(target,name)
        if os.path.exists(src):
            if os.path.islink(dst) or not os.path.isfile(dst) or digest(src) != digest(dst): raise RuntimeError("immutable release differs: " + name)
if os.path.lexists(target): verify()
else:
    temp = tempfile.mkdtemp(prefix=".fleet-stage-",dir=root)
    try:
        shutil.copytree(source,temp,dirs_exist_ok=True)
        os.rename(temp,target)
        verify()
    finally:
        if os.path.exists(temp): shutil.rmtree(temp)
`

func (a *App) stageHostRelease(ctx context.Context, app *sdk.AppCtx, t *Tenant, runtime aptevaRuntimePaths, version string) (string, error) {
	if _, err := validateAptevaVersion(version, false); err != nil {
		return "", err
	}
	cmd := "python3 -c " + sh(stageHostReleaseScript) + " " + sh(filepath.Dir(runtime.Server)) + " " + sh(t.ConfigDir) + " " + sh(version)
	if _, err := a.tenantCommand(ctx, app, t, cmd, 2*time.Minute); err != nil {
		return "", fmt.Errorf("stage host release: %w", err)
	}
	return filepath.Join(t.ConfigDir, "versions", version, "apteva-server"), nil
}

type hostOperation struct {
	Requested      string `json:"requested"`
	Previous       string `json:"previous"`
	PreviousTarget string `json:"previous_target"`
	JobID          string `json:"job_id"`
	PriorJobID     string `json:"prior_job_id"`
}
type hostOperationSnapshot struct {
	Source  *Tenant        `json:"source"`
	Host    *hostOperation `json:"host,omitempty"`
	Startup bool           `json:"host_startup,omitempty"`
}

func (a *App) verifyHostServing(ctx context.Context, app *sdk.AppCtx, t *Tenant, s fleetHostState, version string) error {
	if err := validateFleetHost(s, t); err != nil {
		return err
	}
	if s.Version != version || s.ActiveSlot == nil || s.ActiveSlot.PID < 1 || s.ActiveSlot.Candidate.Version != version || s.ActiveSlot.Candidate.Binary != s.Binary {
		return fmt.Errorf("host committed/active slot does not attest requested version %s", version)
	}
	base, err := a.internalTenantBaseURL(app, t)
	if err != nil {
		return err
	}
	_, err = requireRuntimeHealthVersion(ctx, base, version)
	return err
}

// Resume a recorded job without re-submitting it. On any uncertain outcome the
// snapshot survives Fleet restart and blocks auto-respawn and ordinary recovery.
func (a *App) deployStableHost(ctx context.Context, app *sdk.AppCtx, t *Tenant, s fleetHostState, requested, binary string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, hostDeploymentTimeout)
	defer cancel()
	snapshot := hostOperationSnapshot{Source: t, Host: &hostOperation{Requested: requested, Previous: s.Version, PreviousTarget: t.TargetVersion}}
	if active, err := a.store.activeOperation(t.ID); err != nil {
		return nil, err
	} else if active != nil {
		var prior hostOperationSnapshot
		if err := json.Unmarshal(active.Snapshot, &prior); err != nil {
			return nil, err
		}
		if prior.Host != nil {
			snapshot = prior
		}
	}
	op := snapshot.Host
	if op.PreviousTarget == requested && op.Previous != requested {
		op.PreviousTarget = op.Previous
	}
	if op.Requested != requested {
		return nil, fmt.Errorf("host operation for %s is pending; retry that version before requesting %s", op.Requested, requested)
	}
	checkpoint := func(phase string) error {
		return a.checkpointOperation(t.ID, "host_"+phase, snapshot)
	}
	fenced := func(err error) (any, error) {
		a.requireRecovery(t.ID, err)
		return nil, fmt.Errorf("stable host operation unresolved; retry tenant_update for %s to reconcile; no restart or binary rollback attempted: %w", requested, err)
	}
	if op.JobID == "" {
		// A prior checkpoint with no ID means submission may have reached the
		// host. Only attach a newly observed matching job; never replay POST.
		active, _ := a.store.activeOperation(t.ID)
		var prior hostOperationSnapshot
		if active != nil {
			_ = json.Unmarshal(active.Snapshot, &prior)
		}
		if prior.Host != nil {
			if s.Job == nil || s.Job.ID == op.PriorJobID || s.Job.Version != requested || s.Job.Previous != op.Previous {
				return fenced(errors.New("cannot identify the submitted deployment"))
			}
			op.JobID = s.Job.ID
		} else {
			if s.Job != nil && !hostTerminal(s.Job.Phase) {
				return nil, errors.New("host already has an independent deployment in progress")
			}
			if s.Job != nil && s.Job.Phase == "recovery_required" {
				return nil, errors.New("host requires recovery before deployment")
			}
			if s.Job != nil {
				op.PriorJobID = s.Job.ID
			}
			if err := checkpoint("submitting"); err != nil {
				return nil, err
			}
			if err := a.store.setTargetVersion(t.ID, requested); err != nil {
				return fenced(err)
			}
			next, _, err := a.hostRequest(ctx, app, t, "POST", "/deploy", map[string]string{"binary": binary, "version": requested, "agent_policy": "preserve"})
			if err != nil {
				return fenced(err)
			}
			s = next
			if s.Job == nil || s.Job.ID == "" || s.Job.Version != requested || s.Job.Previous != op.Previous {
				return fenced(errors.New("host returned no matching deployment identity"))
			}
			op.JobID = s.Job.ID
		}
		if err := checkpoint("submitted"); err != nil {
			return fenced(err)
		}
	}
	lastPhase := ""
	for {
		if s.Job == nil || s.Job.ID != op.JobID || s.Job.Version != requested {
			return fenced(errors.New("host deployment identity changed"))
		}
		if s.Job.Phase != lastPhase {
			if err := checkpoint(s.Job.Phase); err != nil {
				return fenced(err)
			}
			_ = a.store.recordEvent(t.ID, "host_deployment_progress", "tool:tenant_update", map[string]any{"job_id": op.JobID, "phase": s.Job.Phase, "version": requested, "message": s.Job.Message})
			lastPhase = s.Job.Phase
		}
		switch s.Job.Phase {
		case "succeeded":
			if err := a.verifyHostServing(ctx, app, t, s, requested); err != nil {
				return fenced(err)
			}
			if err := a.store.setCurrentVersion(t.ID, requested); err != nil {
				return fenced(err)
			}
			if err := a.store.setTargetVersion(t.ID, requested); err != nil {
				return fenced(err)
			}
			if err := a.store.clearOperationLease(t.ID); err != nil {
				return fenced(err)
			}
			if err := a.markHostOperational(t.ID); err != nil {
				return fenced(err)
			}
			updated, _, err := a.store.get(t.ID)
			if err != nil {
				return fenced(err)
			}
			return map[string]any{"tenant": a.publicTenantView(updated), "updated": true, "version": requested, "host_job_id": op.JobID, "note": "host deployment succeeded; serving version and active slot verified"}, nil
		case "failed", "rolled_back":
			if err := a.verifyHostServing(ctx, app, t, s, op.Previous); err != nil {
				return fenced(fmt.Errorf("host reported %s but previous version is unverified: %w", s.Job.Phase, err))
			}
			if err := a.store.setCurrentVersion(t.ID, op.Previous); err != nil {
				return fenced(err)
			}
			if err := a.store.setTargetVersion(t.ID, op.PreviousTarget); err != nil {
				return fenced(err)
			}
			if err := a.store.clearOperationLease(t.ID); err != nil {
				return fenced(err)
			}
			if err := a.markHostOperational(t.ID); err != nil {
				return fenced(err)
			}
			return nil, fmt.Errorf("host deployment %s: %s; previous serving version %s verified", s.Job.Phase, s.Job.Message, op.Previous)
		case "recovery_required":
			return fenced(errors.New(s.Job.Message))
		}
		select {
		case <-ctx.Done():
			return fenced(ctx.Err())
		case <-time.After(time.Second):
		}
		next, present, err := a.hostRequest(ctx, app, t, "GET", "/status", nil)
		if err != nil {
			return fenced(err)
		}
		if !present {
			return fenced(errors.New("stable host disappeared"))
		}
		s = next
	}
}

func (a *App) tryStableHostUpdate(ctx context.Context, app *sdk.AppCtx, t *Tenant, requested string) (bool, any, error) {
	active, err := a.store.activeOperation(t.ID)
	if err != nil {
		return true, nil, err
	}
	var pending hostOperationSnapshot
	if active != nil {
		_ = json.Unmarshal(active.Snapshot, &pending)
	}
	if !hostedTenantExpectedRunning(t.Status) && pending.Host == nil && !pending.Startup {
		return false, nil, nil
	}
	s, present, err := a.hostRequest(ctx, app, t, "GET", "/status", nil)
	if !present {
		if pending.Host != nil || pending.Startup {
			return true, nil, errors.New("recorded stable host disappeared; legacy restart fallback refused")
		}
		return false, nil, nil
	}
	if err != nil {
		return true, nil, fmt.Errorf("stable host unavailable; refusing legacy restart fallback: %w", err)
	}
	if pending.Host != nil {
		out, err := a.deployStableHost(ctx, app, t, s, requested, "")
		return true, out, err
	}
	if s.Job != nil && (!hostTerminal(s.Job.Phase) || s.Job.Phase == "recovery_required") {
		return true, nil, fmt.Errorf("host deployment %s requires reconciliation", s.Job.Phase)
	}
	if err := a.verifyHostServing(ctx, app, t, s, s.Version); err != nil {
		return true, nil, err
	}
	if s.Version == requested {
		if err := a.store.setCurrentVersion(t.ID, requested); err != nil {
			return true, nil, err
		}
		if err := a.store.setTargetVersion(t.ID, requested); err != nil {
			return true, nil, err
		}
		if err := a.checkpointOperation(t.ID, "host_verified", hostOperationSnapshot{Source: t}); err != nil {
			return true, nil, err
		}
		if err := a.store.clearOperationLease(t.ID); err != nil {
			return true, nil, err
		}
		if err := a.markHostOperational(t.ID); err != nil {
			return true, nil, err
		}
		updated, _, err := a.store.get(t.ID)
		return true, map[string]any{"tenant": a.publicTenantView(updated), "updated": false, "version": requested, "reason": "serving runtime and stable host verified at requested version"}, err
	}
	var runtime aptevaRuntimePaths
	if t.IsHosted() {
		runtime, _, err = a.ensureHostedVersionInstalled(app, t.InstanceID, requested)
	} else {
		var cli string
		cli, err = ensureVersionInstalled(ctx, requested)
		runtime, _ = versionedRuntimeFromCLI(cli)
	}
	if err != nil {
		return true, nil, err
	}
	binary, err := a.stageHostRelease(ctx, app, t, runtime, requested)
	if err != nil {
		return true, nil, err
	}
	out, err := a.deployStableHost(ctx, app, t, s, requested, binary)
	return true, out, err
}

func (a *App) markHostOperational(id string) error {
	t, _, err := a.store.get(id)
	if err != nil {
		return err
	}
	if t.Status != StatusStarting && t.Status != StatusDisconnected && t.Status != StatusFailed {
		return nil
	}
	status := StatusActive
	if !a.setupComplete(id) {
		status = StatusSetupPending
	}
	return a.store.setStatus(id, status, "system:host_verified")
}

var errStableHostPending = errors.New("stable host recovery required")

// Startup is distinct from deployment. A cold/recovered host may not have a
// deployment job, so wait for an attested active slot and public health. A
// prepared target is deployed only after recovery of the committed version.
func (a *App) waitTenantStartup(ctx context.Context, app *sdk.AppCtx, t *Tenant, expected string, legacyTimeout time.Duration, exited <-chan struct{}) error {
	deadline := time.Now().Add(legacyTimeout)
	hostSeen := false
	lastPhase := ""
	var lastErr error
	base, err := a.internalTenantBaseURL(app, t)
	if err != nil {
		if _, present, _ := a.hostRequest(ctx, app, t, "JOURNAL", "", nil); present {
			_ = a.checkpointOperation(t.ID, "host_startup_unavailable", hostOperationSnapshot{Source: t, Startup: true})
			a.requireRecovery(t.ID, err)
			return fmt.Errorf("%w: startup tunnel unavailable; host left running: %v", errStableHostPending, err)
		}
		return err
	}
	for {
		s, present, probeErr := a.hostRequest(ctx, app, t, "GET", "/status", nil)
		if present && !hostSeen {
			hostSeen = true
			deadline = time.Now().Add(hostStartupTimeout)
		}
		if present {
			phase := "starting"
			if s.Job != nil {
				phase = s.Job.Phase
			}
			if phase != lastPhase {
				if err := a.checkpointOperation(t.ID, "host_startup_"+phase, hostOperationSnapshot{Source: t, Startup: true}); err != nil {
					return err
				}
				_ = a.store.recordEvent(t.ID, "host_startup_progress", "system", map[string]any{"phase": phase, "version": s.Version})
				lastPhase = phase
			}
			if probeErr == nil && s.Job != nil && s.Job.Phase == "recovery_required" {
				lastErr = errors.New(s.Job.Message)
				break
			}
			if probeErr == nil && (s.Job == nil || hostTerminal(s.Job.Phase)) {
				probeErr = a.verifyHostServing(ctx, app, t, s, s.Version)
				if probeErr == nil {
					if expected == "" || s.Version == expected {
						if err := a.store.setCurrentVersion(t.ID, s.Version); err != nil {
							a.requireRecovery(t.ID, err)
							return fmt.Errorf("%w: %v", errStableHostPending, err)
						}
						return nil
					}
					// The host has safely recovered the prior commit. Change its
					// desired version through deployment, never by editing state.
					_, _, err := a.tryStableHostUpdate(ctx, app, t, expected)
					if err != nil {
						a.requireRecovery(t.ID, err)
						return fmt.Errorf("%w: %v; host left running for recovery", errStableHostPending, err)
					}
					return nil
				}
			}
			lastErr = probeErr
		} else if hostSeen {
			lastErr = errors.New("host disappeared during startup")
		} else {
			if expected != "" {
				_, probeErr = requireRuntimeHealthVersion(ctx, base, expected)
			} else {
				var ok bool
				ok, _, _, probeErr = probeHealth(ctx, base, "")
				if probeErr == nil && !ok {
					probeErr = errors.New("runtime is not ready")
				}
			}
			if probeErr == nil {
				return nil
			}
			lastErr = probeErr
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-exited:
			lastErr = errors.New("tenant launcher exited before readiness")
		case <-time.After(500 * time.Millisecond):
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = errors.New("startup deadline exceeded")
	}
	if hostSeen {
		a.requireRecovery(t.ID, lastErr)
		return fmt.Errorf("%w: %v; host left running for recovery", errStableHostPending, lastErr)
	}
	return fmt.Errorf("tenant did not become ready: %w", lastErr)
}
