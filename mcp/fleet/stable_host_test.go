package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// Exercise the actual Unix HTTP protocol and public health endpoint, rather
// than mocking Fleet's deployment/verification functions.
type hostTestRig struct {
	mu            sync.Mutex
	app           *App
	ctx           *sdk.AppCtx
	tenant        *Tenant
	state         fleetHostState
	healthVersion string
	ready         bool
	posts         int
	statusReads   int
	onDeploy      func(*hostTestRig)
	onStatus      func(*hostTestRig)
}

func newHostTestRig(t *testing.T) *hostTestRig {
	t.Helper()
	r := &hostTestRig{healthVersion: "0.90.8", ready: true}
	r.app, r.ctx = newTestApp(t)
	id := seedTenant(t, r.app, "hosted", StatusActive)
	r.tenant, _, _ = r.app.store.get(id)
	// Unix sockets on macOS have a short path limit.
	home, err := os.MkdirTemp("/tmp", "fleet-host-")
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if req.URL.Path != "/api/health" {
			http.NotFound(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": r.ready, "cli": r.healthVersion, "apteva": r.healthVersion})
	}))
	t.Cleanup(health.Close)
	if _, err := r.app.store.db.Exec(`UPDATE fleet_tenants SET config_dir=?,base_url=?,current_version=?,target_version=? WHERE id=?`, home, health.URL, "0.90.8", "0.90.8", id); err != nil {
		t.Fatal(err)
	}
	r.tenant, _, _ = r.app.store.get(id)
	r.state = fleetHostState{Protocol: 1, Home: home, DBPath: filepath.Join(home, "apteva.db"), ListenAddr: strings.TrimPrefix(health.URL, "http://"), PID: os.Getpid(), HostBinary: "/release/apteva-host"}
	r.setServing("0.90.8")
	if err := os.MkdirAll(filepath.Join(home, "host"), 0700); err != nil {
		t.Fatal(err)
	}
	r.writeJournal(t)
	listener, err := net.Listen("unix", filepath.Join(home, "host", "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch req.Method + " " + req.URL.Path {
		case "GET /status":
			r.statusReads++
			if r.onStatus != nil {
				r.onStatus(r)
			}
		case "POST /deploy":
			var input struct{ Binary, Version, AgentPolicy string }
			var payload map[string]string
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			input.Binary, input.Version, input.AgentPolicy = payload["binary"], payload["version"], payload["agent_policy"]
			if input.Binary != filepath.Join(home, "versions", input.Version, "apteva-server") || input.AgentPolicy != "preserve" {
				t.Errorf("invalid deployment: %+v", input)
			}
			if _, err := os.Stat(input.Binary); err != nil {
				t.Errorf("release not staged: %v", err)
			}
			r.posts++
			r.state.Job = &fleetHostJob{ID: fmt.Sprintf("job-%d", r.posts), Version: input.Version, Previous: r.state.Version, Phase: "warming"}
			if r.onDeploy != nil {
				r.onDeploy(r)
			} else {
				r.setServing(input.Version)
				r.state.Job.Phase = "succeeded"
			}
		default:
			http.NotFound(w, req)
			return
		}
		_ = json.NewEncoder(w).Encode(r.state)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	versions := t.TempDir()
	t.Setenv("FLEET_VERSIONS_ROOT", versions)
	writeFakeVersionedRuntime(t, versions, "0.90.8")
	writeFakeVersionedRuntime(t, versions, "0.91.0")
	return r
}

// Call while holding r.mu once the HTTP servers have started.
func (r *hostTestRig) setServing(version string) {
	r.state.Version = version
	r.state.Binary = filepath.Join(r.state.Home, "versions", version, "apteva-server")
	// Build through JSON to avoid a second copy of the anonymous slot type.
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "candidate": map[string]string{"version": version, "binary": r.state.Binary}})
	_ = json.Unmarshal(b, &r.state.ActiveSlot)
	r.healthVersion = version
}
func (r *hostTestRig) writeJournal(t *testing.T) {
	t.Helper()
	b, _ := json.Marshal(r.state)
	if err := os.WriteFile(filepath.Join(r.state.Home, "host", "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStableHostUpdateStagesAndVerifiesRelease(t *testing.T) {
	r := newHostTestRig(t)
	r.onDeploy = func(r *hostTestRig) { r.state.Job.Phase = "warming" }
	r.onStatus = func(r *hostTestRig) {
		if r.state.Job != nil {
			r.setServing(r.state.Job.Version)
			r.state.Job.Phase = "succeeded"
		}
	}
	out, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["host_job_id"] != "job-1" {
		t.Fatalf("result: %v", out)
	}
	got, _, _ := r.app.store.get(r.tenant.ID)
	if got.CurrentVersion != "0.91.0" || got.TargetVersion != "0.91.0" {
		t.Fatalf("version: %+v", got)
	}
	var phases string
	rows, err := r.app.store.db.Query(`SELECT payload FROM fleet_events WHERE tenant_id=? AND kind='host_deployment_progress' ORDER BY id`, r.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		phases += payload
	}
	if !strings.Contains(phases, "warming") || !strings.Contains(phases, "succeeded") {
		t.Fatalf("missing progress: %s", phases)
	}
	if op, _ := r.app.store.activeOperation(r.tenant.ID); op != nil {
		t.Fatalf("completed operation retained: %+v", op)
	}
	if r.posts != 1 {
		t.Fatalf("posts=%d", r.posts)
	}
}

func TestStableHostRollbackRequiresHealthAndActiveSlot(t *testing.T) {
	for _, lying := range []bool{false, true} {
		t.Run(fmt.Sprint(lying), func(t *testing.T) {
			r := newHostTestRig(t)
			data := []byte("writes made after update began")
			if err := os.WriteFile(r.state.DBPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			r.onDeploy = func(r *hostTestRig) {
				r.state.Job.Phase = "rolled_back"
				r.state.Job.Message = "candidate failed"
				if lying {
					r.healthVersion = "0.91.0"
				}
			}
			_, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"})
			if err == nil {
				t.Fatal("failed deployment accepted")
			}
			op, _ := r.app.store.activeOperation(r.tenant.ID)
			if lying {
				if op == nil || op.Phase != "recovery_required" {
					t.Fatalf("unverified rollback not fenced: %+v", op)
				}
				r.mu.Lock()
				r.healthVersion = "0.90.8"
				r.mu.Unlock()
				if err := r.app.store.setStatus(r.tenant.ID, StatusFailed, "test"); err != nil {
					t.Fatal(err)
				}
				if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"}); err == nil || !strings.Contains(err.Error(), "previous serving version 0.90.8 verified") {
					t.Fatalf("rollback retry: %v", err)
				}
				got, _, _ := r.app.store.get(r.tenant.ID)
				if got.Status == StatusFailed {
					t.Fatal("verified serving rollback retained failed status")
				}
			} else {
				if !strings.Contains(err.Error(), "previous serving version 0.90.8 verified") || op != nil {
					t.Fatalf("rollback: %v, op=%+v", err, op)
				}
				got, _, _ := r.app.store.get(r.tenant.ID)
				if got.TargetVersion != "0.90.8" {
					t.Fatalf("target: %s", got.TargetVersion)
				}
			}
			got, _ := os.ReadFile(r.state.DBPath)
			if string(got) != string(data) {
				t.Fatal("live database was replaced")
			}
			if r.posts != 1 {
				t.Fatal("rollback submitted a second deployment")
			}
		})
	}
}

func TestStableHostTimeoutResumesSameJobAfterFleetRestart(t *testing.T) {
	r := newHostTestRig(t)
	r.onDeploy = func(r *hostTestRig) { r.state.Job.Phase = "activating" }
	done, err := r.app.beginTenantOperation(r.tenant.ID, "update")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	handled, _, err := r.app.tryStableHostUpdate(ctx, r.ctx, r.tenant, "0.91.0")
	cancel()
	done()
	if !handled || err == nil {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := r.app.beginTenantOperation(r.tenant.ID, "health probe"); err == nil {
		t.Fatal("respawn fence missing")
	}
	// Recreate in-memory operation state, retaining only persisted rows.
	r.app.operations = nil
	r.mu.Lock()
	r.setServing("0.91.0")
	r.state.Job.Phase = "succeeded"
	r.mu.Unlock()
	if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"}); err != nil {
		t.Fatal(err)
	}
	if r.posts != 1 {
		t.Fatalf("deployment was replayed: %d", r.posts)
	}
	if op, _ := r.app.store.activeOperation(r.tenant.ID); op != nil {
		t.Fatalf("fence retained: %+v", op)
	}
}

func TestStableHostRejectsChangedJobAndRecoveryRequired(t *testing.T) {
	for _, phase := range []string{"recovery_required", "changed_identity"} {
		t.Run(phase, func(t *testing.T) {
			r := newHostTestRig(t)
			r.onDeploy = func(r *hostTestRig) { r.state.Job.Phase = "activating" }
			r.onStatus = func(r *hostTestRig) {
				if r.state.Job != nil {
					if phase == "changed_identity" {
						r.state.Job.ID = "other"
					} else {
						r.state.Job.Phase = phase
					}
				}
			}
			_, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"})
			if err == nil {
				t.Fatal("unsafe deployment accepted")
			}
			if op, _ := r.app.store.activeOperation(r.tenant.ID); op == nil || op.Phase != "recovery_required" {
				t.Fatalf("missing recovery fence: %+v", op)
			}
		})
	}
}

func TestStableHostVersionNoopVerifiesRuntime(t *testing.T) {
	r := newHostTestRig(t)
	r.mu.Lock()
	r.healthVersion = "0.91.0"
	r.mu.Unlock()
	if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.90.8"}); err == nil {
		t.Fatal("cached version trusted despite health mismatch")
	}
	if r.posts != 0 {
		t.Fatal("deployed without verifying current runtime")
	}
}

func TestStableHostJournalNeverFallsBackToLegacy(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "stale", StatusActive)
	tn, _, _ := app.store.get(id)
	if err := os.MkdirAll(filepath.Join(tn.ConfigDir, "host"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tn.ConfigDir, "host", "state.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.91.0"}); err == nil || !strings.Contains(err.Error(), "refusing legacy restart") {
		t.Fatalf("unsafe fallback: %v", err)
	}
	if len(app.procs) != 0 {
		t.Fatal("legacy process started")
	}
}

func TestHostStartupCancellationTracksProgressWithoutStoppingHost(t *testing.T) {
	r := newHostTestRig(t)
	r.mu.Lock()
	r.state.ActiveSlot = nil
	r.ready = false
	r.mu.Unlock()
	done, err := r.app.beginTenantOperation(r.tenant.ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	err = r.app.waitTenantStartup(ctx, r.ctx, r.tenant, "0.90.8", 10*time.Millisecond, nil)
	cancel()
	done()
	if !errors.Is(err, errStableHostPending) {
		t.Fatalf("startup: %v", err)
	}
	op, _ := r.app.store.activeOperation(r.tenant.ID)
	if op == nil || op.Phase != "recovery_required" || !strings.Contains(string(op.Snapshot), "host_startup") {
		t.Fatalf("startup progress missing: %+v", op)
	}
	r.mu.Lock()
	r.setServing("0.90.8")
	r.ready = true
	r.mu.Unlock()
	if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.90.8"}); err != nil {
		t.Fatal(err)
	}
	if r.posts != 0 {
		t.Fatal("recovered host unnecessarily redeployed")
	}
}

func TestStageHostReleaseNeverOverwritesExistingBinary(t *testing.T) {
	r := newHostTestRig(t)
	runtime, _ := versionedRuntimeFromCLI(tenantAptevaBin("0.91.0"))
	path, err := r.app.stageHostRelease(context.Background(), r.ctx, r.tenant, runtime, "0.91.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("immutable existing server"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.stageHostRelease(context.Background(), r.ctx, r.tenant, runtime, "0.91.0"); err == nil {
		t.Fatal("replaced immutable release")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "immutable existing server" {
		t.Fatal("existing server overwritten")
	}
}

func TestStableHostLostSubmissionResponseAttachesWithoutReplay(t *testing.T) {
	r := newHostTestRig(t)
	done, err := r.app.beginTenantOperation(r.tenant.ID, "update")
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := versionedRuntimeFromCLI(tenantAptevaBin("0.91.0"))
	binary, err := r.app.stageHostRelease(context.Background(), r.ctx, r.tenant, runtime, "0.91.0")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := hostOperationSnapshot{Source: r.tenant, Host: &hostOperation{Requested: "0.91.0", Previous: "0.90.8", PreviousTarget: "0.90.8"}}
	if err := r.app.checkpointOperation(r.tenant.ID, "host_submitting", snapshot); err != nil {
		t.Fatal(err)
	}
	// The host accepts the submission, but Fleet loses the response before
	// persisting the returned deployment ID.
	if _, _, err := r.app.hostRequest(context.Background(), r.ctx, r.tenant, "POST", "/deploy", map[string]string{"binary": binary, "version": "0.91.0", "agent_policy": "preserve"}); err != nil {
		t.Fatal(err)
	}
	r.app.requireRecovery(r.tenant.ID, errors.New("submission response lost"))
	done()
	if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.90.7"}); err == nil {
		t.Fatal("replaced an unresolved target")
	}
	if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"}); err != nil {
		t.Fatal(err)
	}
	if r.posts != 1 {
		t.Fatalf("submission replayed: %d", r.posts)
	}
}

func TestStableHostRejectsDifferentInstallationAndSlot(t *testing.T) {
	for _, field := range []string{"home", "database", "port", "protocol", "slot"} {
		t.Run(field, func(t *testing.T) {
			r := newHostTestRig(t)
			r.mu.Lock()
			switch field {
			case "home":
				r.state.Home = "/other"
			case "database":
				r.state.DBPath = "/other/apteva.db"
			case "port":
				r.state.ListenAddr = "127.0.0.1:1"
			case "protocol":
				r.state.Protocol = 2
			case "slot":
				r.state.ActiveSlot.Candidate.Version = "0.91.0"
			}
			r.mu.Unlock()
			if _, err := r.app.toolUpdate(r.ctx, map[string]any{"tenant_id": r.tenant.ID, "version": "0.91.0"}); err == nil {
				t.Fatal("unverified host identity accepted")
			}
			if r.posts != 0 {
				t.Fatal("deployment reached a different/unverified installation")
			}
		})
	}
}

func TestHostRecoveryOnlyReconcilesTerminalServingState(t *testing.T) {
	r := newHostTestRig(t)
	done, err := r.app.beginTenantOperation(r.tenant.ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.app.checkpointOperation(r.tenant.ID, "host_startup_starting", hostOperationSnapshot{Source: r.tenant, Startup: true}); err != nil {
		t.Fatal(err)
	}
	r.app.requireRecovery(r.tenant.ID, errors.New("startup interrupted"))
	done()
	r.mu.Lock()
	r.state.Job = &fleetHostJob{ID: "job-1", Version: "0.91.0", Previous: "0.90.8", Phase: "activating"}
	r.mu.Unlock()
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/tenants/"+r.tenant.ID+"/recover", strings.NewReader(`{"confirm":true}`))
		r.app.httpRecoverOperation(w, req)
		return w
	}
	if w := request(); w.Code != 409 {
		t.Fatalf("recovery while deployment active: %d %s", w.Code, w.Body.String())
	}
	if _, err := requireRuntimeHealthVersion(context.Background(), r.tenant.BaseURL, "0.90.8"); err != nil {
		t.Fatal("recovery stopped the host")
	}
	r.mu.Lock()
	r.state.Job.Phase = "rolled_back"
	r.mu.Unlock()
	if w := request(); w.Code != 200 {
		t.Fatalf("recovery: %d %s", w.Code, w.Body.String())
	}
	if op, _ := r.app.store.activeOperation(r.tenant.ID); op != nil {
		t.Fatalf("verified recovery retained fence: %+v", op)
	}
	if r.posts != 0 {
		t.Fatal("recovery submitted a deployment")
	}
}

func TestRetentionKeepsStableHostBootstrapCache(t *testing.T) {
	r := newHostTestRig(t)
	dir := filepath.Join(versionsRoot(), "0.80.0")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-45 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	plan, err := r.app.retentionPlan()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range plan {
		if candidate.Kind == "version" {
			t.Fatalf("host bootstrap cache offered for removal: %+v", candidate)
		}
	}
}

func TestHostLaunchEnvironmentUsesRecordedPortAndCandidateCore(t *testing.T) {
	home := t.TempDir()
	s := fleetHostState{Home: home, DBPath: filepath.Join(home, "apteva.db"), ListenAddr: "127.0.0.1:7109", Binary: "/old-release/apteva-server"}
	values := envMap(stableHostLaunchEnv([]string{"PORT=5280", "CORE_CMD=/old-release/apteva-core", "APTEVA_CORE_BIN=/old-release/apteva-core"}, s))
	if values["PORT"] != "7109" || values["APTEVA_BIND"] != "127.0.0.1" || values["DB_PATH"] != s.DBPath || values["DATA_DIR"] != home {
		t.Fatalf("host identity changed: %+v", values)
	}
	if values["CORE_CMD"] != filepath.Join(home, "bin", "apteva-core") || values["APTEVA_CORE_BIN"] != "" {
		t.Fatal("bootstrap Core was pinned across deployments")
	}
}

// Instances executes the real remote Python adapter against the test Unix
// socket. This covers quoting, socket privacy, error/absence handling and the
// tunnel used for serving-version verification without a production VPS.
type hostCommandPlatform struct {
	tk.BasePlatformClient
	port     int
	commands []string
}

func (p *hostCommandPlatform) CallApp(app, tool string, input map[string]any) (json.RawMessage, error) {
	if app != "instances" {
		return nil, fmt.Errorf("unexpected app %s", app)
	}
	switch tool {
	case "instance_run_command":
		command := input["cmd"].(string)
		p.commands = append(p.commands, command)
		cmd := exec.Command("sh", "-c", command)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			code = 1
		}
		return wrappedToolResult(map[string]any{"output": string(out), "exit_code": code}), nil
	case "instance_open_tunnel":
		return wrappedToolResult(map[string]any{"local_host": "127.0.0.1", "local_port": p.port}), nil
	case "instance_close_tunnel":
		return wrappedToolResult(map[string]any{"closed": true}), nil
	default:
		return nil, fmt.Errorf("unexpected tool %s", tool)
	}
}

func TestRemoteStableHostAdapterDeploysAndRedactsJournal(t *testing.T) {
	r := newHostTestRig(t)
	p := &hostCommandPlatform{port: portFromTenant(r.tenant)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	tn := *r.tenant
	tn.InstanceID = 3
	// Add a key to the on-disk journal. It must not reach tool stdout.
	b, _ := json.Marshal(r.state)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	raw["active_slot"].(map[string]any)["proxy_key"] = "private-proxy-key"
	b, _ = json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(tn.ConfigDir, "host", "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	journal, present, err := r.app.hostRequest(context.Background(), ctx, &tn, "JOURNAL", "", nil)
	if err != nil || !present || journal.Version != "0.90.8" {
		t.Fatalf("journal: %+v %v", journal, err)
	}
	runtime, _ := versionedRuntimeFromCLI(tenantAptevaBin("0.91.0"))
	binary, err := r.app.stageHostRelease(context.Background(), ctx, &tn, runtime, "0.91.0")
	if err != nil {
		t.Fatal(err)
	}
	done, err := r.app.beginTenantOperation(tn.ID, "update")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	s, _, err := r.app.hostRequest(context.Background(), ctx, &tn, "GET", "/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.deployStableHost(context.Background(), ctx, &tn, s, "0.91.0", binary); err != nil {
		t.Fatal(err)
	}
	if r.posts != 1 {
		t.Fatalf("posts=%d", r.posts)
	}
	for _, command := range p.commands {
		if strings.Contains(command, "kill -") || strings.Contains(command, "setsid") {
			t.Fatal("host update executed legacy lifecycle command")
		}
	}
}

func TestHostColdStartUsesJournalAndPreservesIncompatibleFallback(t *testing.T) {
	for _, requested := range []string{"0.91.0", "0.90.8"} {
		t.Run(requested, func(t *testing.T) {
			app, ctx := newTestApp(t)
			// Use a short, canonical home for the subprocess Unix listener.
			root, err := os.MkdirTemp("/tmp", "fleet-cold-")
			if err != nil {
				t.Fatal(err)
			}
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			t.Setenv("FLEET_DATA_ROOT", root)
			versions := t.TempDir()
			t.Setenv("FLEET_VERSIONS_ROOT", versions)
			writeLifecycleRuntime(t, versions, "0.90.8", false, true, false)
			activeRuntime := writeLifecycleRuntime(t, versions, "0.91.0", false, true, false)
			id := seedTenant(t, app, "tenant", StatusStopped)
			tn, _, _ := app.store.get(id)
			port, err := allocatePort()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET base_url=?,current_version='0.91.0',target_version=? WHERE id=?`, fmt.Sprintf("http://127.0.0.1:%d", port), requested, id); err != nil {
				t.Fatal(err)
			}
			tn, _, _ = app.store.get(id)
			if err := os.MkdirAll(filepath.Join(tn.ConfigDir, "host"), 0700); err != nil {
				t.Fatal(err)
			}
			hostBinary := filepath.Join(root, "test-host")
			state := fleetHostState{Protocol: 1, Home: tn.ConfigDir, DBPath: filepath.Join(tn.ConfigDir, "apteva.db"), ListenAddr: fmt.Sprintf("127.0.0.1:%d", port), PID: 1, HostBinary: hostBinary, Binary: activeRuntime.Server, Version: "0.91.0"}
			b, _ := json.Marshal(state)
			if err := os.WriteFile(filepath.Join(tn.ConfigDir, "host", "state.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/usr/bin/env python3
import http.server, json, os, signal, socketserver, threading, time
home=os.environ["APTEVA_HOME"]
path=os.path.join(home,"host","state.json")
with open(path) as f: state=json.load(f)
state["pid"]=os.getpid()
state["active_slot"]={"pid":os.getpid(),"candidate":{"version":state["version"],"binary":state["binary"]}}
with open(os.path.join(home,"host-env.json"),"w") as f: json.dump(dict(os.environ),f)
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200);self.end_headers()
        value=state if self.path=="/status" else {"ok":True,"cli":state["version"],"apteva":state["version"]}
        self.wfile.write(json.dumps(value).encode())
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length","0")))
        self.send_response(409);self.end_headers();self.wfile.write(b"incompatible schema; maintenance migration required")
    def log_message(self,*args): pass
class Unix(socketserver.UnixStreamServer): pass
control=os.path.join(home,"host","control.sock")
if os.path.exists(control): os.unlink(control)
unix=Unix(control,Handler)
threading.Thread(target=unix.serve_forever,daemon=True).start()
time.sleep(0.2)
http.server.HTTPServer((os.environ["APTEVA_BIND"],int(os.environ["PORT"])),Handler).serve_forever()
`
			if err := os.WriteFile(hostBinary, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.stopTenantBy(tn.Slug, tn.ConfigDir, port, time.Second) })
			_, err = app.toolStart(ctx, map[string]any{"tenant_id": id})
			if requested == "0.90.8" {
				if err == nil || !strings.Contains(err.Error(), "incompatible schema") {
					t.Fatalf("fallback: %v", err)
				}
				if _, err := os.Stat(filepath.Join(tn.ConfigDir, "launched-0.90.8.json")); !os.IsNotExist(err) {
					t.Fatal("older CLI was launched")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := requireRuntimeHealthVersion(context.Background(), tn.BaseURL, "0.91.0"); err != nil {
				t.Fatalf("committed host was terminated: %v", err)
			}
			envBytes, err := os.ReadFile(filepath.Join(tn.ConfigDir, "host-env.json"))
			if err != nil {
				t.Fatal(err)
			}
			var env map[string]string
			if err := json.Unmarshal(envBytes, &env); err != nil {
				t.Fatal(err)
			}
			if env["CORE_CMD"] != filepath.Join(tn.ConfigDir, "bin", "apteva-core") || env["APTEVA_CORE_BIN"] != "" {
				t.Fatalf("host core pinned to stale bootstrap version: %s", env["CORE_CMD"])
			}
		})
	}
}
