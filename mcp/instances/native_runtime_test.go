package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/ssh"
)

type nativeFake struct {
	mu                     sync.Mutex
	starts, stops, deletes int
	states                 map[string]nativeObservation
	health                 *httptest.Server
	loseStart, failStop    bool
	prepareGate            <-chan struct{}
}

func (f *nativeFake) Probe(context.Context, *Instance, string) (nativeProbe, error) {
	return nativeProbe{Platform: "linux-amd64", HostKey: "physical-host-a", MemoryMB: 2048, DiskMB: 2048}, nil
}
func (f *nativeFake) Prepare(c context.Context, _ *Instance, d nativeRecord, _ string) error {
	if f.prepareGate != nil {
		select {
		case <-c.Done():
			return c.Err()
		case <-f.prepareGate:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[d.ID] = nativeObservation{State: "prepared", Prepared: true}
	return nil
}
func (f *nativeFake) Start(_ context.Context, _ *Instance, d nativeRecord, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.states[d.ID] = nativeObservation{State: "running"}
	if f.loseStart {
		return errors.New("lost response contains sensitive-credential")
	}
	return nil
}
func (f *nativeFake) Inspect(_ context.Context, _ *Instance, d nativeRecord) (nativeObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[d.ID], nil
}
func (f *nativeFake) Stop(_ context.Context, _ *Instance, d nativeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.failStop {
		return errors.New("SSH timeout sensitive-credential")
	}
	f.states[d.ID] = nativeObservation{State: "stopped", Stopped: true}
	return nil
}
func (f *nativeFake) Delete(_ context.Context, _ *Instance, d nativeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return nil
}
func (f *nativeFake) Logs(context.Context, *Instance, nativeRecord, string, int) ([]string, string, error) {
	return []string{"token sensitive-credential config nested-secret quoted secret ' \\ \" value", strings.Repeat("long ", 1000)}, "cursor-1", nil
}
func testELF() []byte {
	body := make([]byte, 64)
	copy(body, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 3})
	binary.LittleEndian.PutUint16(body[16:], 2)
	binary.LittleEndian.PutUint16(body[18:], 62)
	binary.LittleEndian.PutUint32(body[20:], 1)
	binary.LittleEndian.PutUint16(body[52:], 64)
	return body
}
func newNativeTest(t *testing.T) (*nativeBackend, *nativeFake, nativeRequest) {
	t.Helper()
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	root := t.TempDir()
	c, cancel := context.WithCancel(context.Background())
	f := &nativeFake{states: map[string]nativeObservation{}}
	f.health = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sensitive-credential" {
			http.Error(w, "bad token", 401)
			return
		}
		if r.URL.Path == "/manifest" {
			writeNative(w, map[string]any{"name": "fixture-app", "version": "1.0.0"})
			return
		}
		if r.URL.Path == "/_apteva/runtime/app/describe" {
			writeNative(w, nativeAppCaps{PassiveStart: true, Drain: true, Routing: "key"})
			return
		}
		w.WriteHeader(200)
	}))
	b := &nativeBackend{db: db, root: root, installID: 40, transport: f, ctx: c, cancel: cancel, locks: map[string]*sync.Mutex{}, jobs: map[string]context.CancelFunc{}}
	b.tunnels = newTunnelRegistry(func(*Instance, string) (net.Conn, error) {
		return net.Dial("tcp", strings.TrimPrefix(f.health.URL, "http://"))
	})
	inst, e := dbCreateInstance(db, CreateInstanceInput{Name: "native-test", Provider: "external", Status: "ready", Platform: "linux", SSHUser: "root", SSHHost: "test.invalid"})
	if e != nil {
		t.Fatal(e)
	}
	a := nativeArtifact{ID: "fixture", AppName: "fixture-app", AppVersion: "1.0.0", Platform: "linux-amd64", Executable: "app", Files: []nativeArtifactFile{{Path: "app", SHA256: nativeHash(string(testELF())), Size: 64}, {Path: "migrations/001.sql", SHA256: nativeHash("create table example(id integer);"), Size: int64(len("create table example(id integer);"))}}, AppCapabilities: nativeAppCaps{PassiveStart: true, Drain: true, Routing: "key"}}
	if e = b.registerArtifact(a); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(b.artifactDir(a.ID), "app"), testELF(), 0600)
	os.MkdirAll(filepath.Join(b.artifactDir(a.ID), "migrations"), 0700)
	os.WriteFile(filepath.Join(b.artifactDir(a.ID), "migrations/001.sql"), []byte("create table example(id integer);"), 0600)
	if e = b.sealArtifact(a); e != nil {
		t.Fatal(e)
	}
	req := nativeRequest{Contract: nativeContract, OperationID: "operation-1", IdempotencyKey: "operation-1:deployment-1", DeploymentID: "deployment-1", InstallID: 52, Generation: 1, ProjectID: "project-a", Target: nativeTarget{InstallID: 40, Type: "instances.instance", ID: strconv.FormatInt(inst.ID, 10)}, Options: map[string]any{"artifact_id": "fixture", "gateway_url": "https://gateway.test"}, Manifest: sdk.Manifest{Name: "fixture-app", Version: "1.0.0", Runtime: sdk.Runtime{Kind: "source"}}}
	t.Cleanup(func() { cancel(); b.tunnels.closeAll(); f.health.Close() })
	return b, f, req
}
func nativeCall(t *testing.T, b *nativeBackend, action string, req nativeRequest) (nativeExecution, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, nativePrefix+"/"+action, strings.NewReader(nativeJSON(req)))
	r.Header.Set("Idempotency-Key", req.IdempotencyKey+":"+action)
	w := httptest.NewRecorder()
	b.serveHTTP(w, r)
	var out nativeExecution
	if w.Code == 200 {
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
	}
	return out, w
}
func prepareNativeTest(t *testing.T, b *nativeBackend, req nativeRequest) nativeExecution {
	t.Helper()
	out, w := nativeCall(t, b, "prepare", req)
	if w.Code != 200 || out.State != "preparing" {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d, e := b.record(req.DeploymentID)
		if e == nil && d.Execution.State == "prepared" {
			return d.Execution
		}
		time.Sleep(time.Millisecond)
	}
	d, e := b.record(req.DeploymentID)
	t.Fatalf("prepare did not finish: state=%s err=%v", d.Execution.State, e)
	return out
}
func startNativeRequest(req nativeRequest) nativeRequest {
	req.Environment = map[string]string{"APTEVA_INSTALL_ID": strconv.FormatInt(req.InstallID, 10), "APTEVA_DEPLOYMENT_ID": req.DeploymentID, "APTEVA_DEPLOYMENT_GENERATION": strconv.FormatInt(req.Generation, 10), "APTEVA_PROJECT_ID": req.ProjectID, "APTEVA_GATEWAY_URL": "https://gateway.test", "APTEVA_APP_TOKEN": "sensitive-credential", "APTEVA_OUTBOUND_TOKEN": "sensitive-credential", "APTEVA_RUNTIME_MODE": "standby", "APTEVA_APP_CONFIG": `{"password":"nested-secret"}`, "QUOTED": "quoted secret ' \\ \" value"}
	return req
}
func TestNativeDeploymentLifecycleIdempotencyAndSecrets(t *testing.T) {
	b, f, req := newNativeTest(t)
	req.Execution = prepareNativeTest(t, b, req)
	repeated, w := nativeCall(t, b, "prepare", req)
	if w.Code != 200 || repeated.Handle != req.Execution.Handle {
		t.Fatal("prepare retry changed identity")
	}
	start := startNativeRequest(req)
	out, w := nativeCall(t, b, "start", start)
	if w.Code != 200 || out.State != "starting" || !out.AppCapabilities.PassiveStart {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	out, w = nativeCall(t, b, "inspect", req)
	if w.Code != 200 || !out.Ready || out.State != "running" {
		t.Fatal("inspection did not confirm authenticated readiness")
	}
	_, w = nativeCall(t, b, "start", start)
	if w.Code != 200 || f.starts != 1 {
		t.Fatal("duplicate start created another process")
	}
	start.Environment["QUOTED"] = "changed"
	_, w = nativeCall(t, b, "start", start)
	if w.Code != 409 {
		t.Fatal("changed environment accepted")
	}
	d, _ := b.record(req.DeploymentID)
	raw, _ := os.ReadFile(b.secretPath(d))
	if bytes.Contains(raw, []byte("sensitive-credential")) {
		t.Fatal("environment persisted in plaintext")
	}
	plain, e := b.readEnvironment(d)
	if e != nil || plain["DB_PATH"] != d.DataDir+"/app.db" {
		t.Fatal("remote data mapping missing")
	}
	var sqlRaw string
	b.db.QueryRow(`SELECT record FROM native_deployments WHERE id=?`, d.ID).Scan(&sqlRaw)
	if strings.Contains(sqlRaw, "sensitive-credential") {
		t.Fatal("secret leaked into journal")
	}
	logs := req
	logs.Options = map[string]any{"limit": 100}
	_, w = nativeCall(t, b, "logs", logs)
	if strings.Contains(w.Body.String(), "sensitive-credential") || strings.Contains(w.Body.String(), "nested-secret") {
		t.Fatal("log secret leak")
	}
	if !strings.Contains(w.Body.String(), "redacted") {
		t.Fatal("redaction missing")
	}
	out, w = nativeCall(t, b, "stop", req)
	if w.Code != 200 || !out.StoppedConfirmed || out.Ready {
		t.Fatal("stop not confirmed")
	}
	out, w = nativeCall(t, b, "start", startNativeRequest(req))
	if w.Code != 200 || out.State != "stopped" || f.starts != 1 {
		t.Fatal("duplicate start resurrected stopped service")
	}
	out, w = nativeCall(t, b, "delete", req)
	if w.Code != 200 || out.State != "deleted" || f.deletes != 1 {
		t.Fatal("delete failed")
	}
	_, w = nativeCall(t, b, "delete", req)
	if w.Code != 200 || f.deletes != 1 {
		t.Fatal("delete not idempotent")
	}
}
func TestNativeLostStartResponseAndRestartRecovery(t *testing.T) {
	b, f, req := newNativeTest(t)
	req.Execution = prepareNativeTest(t, b, req)
	f.loseStart = true
	out, w := nativeCall(t, b, "start", startNativeRequest(req))
	if w.Code != 200 || out.State != "unknown" || out.Ready || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatal("unknown start was not safely recorded")
	}
	b.tunnels.closeAll()
	replacement := nativeBackend{db: b.db, root: b.root, installID: b.installID, transport: f, ctx: b.ctx, cancel: b.cancel, locks: map[string]*sync.Mutex{}, jobs: map[string]context.CancelFunc{}, tunnels: newTunnelRegistry(b.tunnels.dial)}
	defer replacement.tunnels.closeAll()
	out, w = nativeCall(t, &replacement, "inspect", req)
	if w.Code != 200 || !out.Ready || out.Endpoint != req.Execution.Endpoint {
		t.Fatalf("restart recovery: %d %s", w.Code, w.Body.String())
	}
	_, w = nativeCall(t, &replacement, "start", startNativeRequest(req))
	if w.Code != 200 || f.starts != 1 {
		t.Fatal("recovery duplicated process")
	}
}
func TestNativeUnknownStopBlocksDeleteAndReplacement(t *testing.T) {
	b, f, req := newNativeTest(t)
	req.Execution = prepareNativeTest(t, b, req)
	nativeCall(t, b, "start", startNativeRequest(req))
	f.failStop = true
	out, w := nativeCall(t, b, "delete", req)
	if w.Code != 200 || out.StoppedConfirmed || out.State != "unknown" || f.deletes != 0 {
		t.Fatal("unknown stop deleted resource")
	}
}
func TestNativeTargetArtifactAndRetryRejection(t *testing.T) {
	for _, change := range []struct {
		name string
		fn   func(*nativeRequest)
	}{
		{"owner", func(r *nativeRequest) { r.Target.InstallID = 99 }},
		{"local", func(r *nativeRequest) { r.Target.ID = "0" }},
		{"namespace", func(r *nativeRequest) { r.Target.Type = "compute.machine" }},
		{"missing artifact", func(r *nativeRequest) { r.Options["artifact_id"] = "unknown" }},
		{"wrong release", func(r *nativeRequest) { r.Manifest.Version = "2.0.0" }},
		{"missing gateway", func(r *nativeRequest) { delete(r.Options, "gateway_url") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			b, _, req := newNativeTest(t)
			change.fn(&req)
			_, w := nativeCall(t, b, "validate-target", req)
			if w.Code < 400 {
				t.Fatalf("accepted %s", change.name)
			}
		})
	}
	b, _, req := newNativeTest(t)
	prepareNativeTest(t, b, req)
	req.Generation++
	_, w := nativeCall(t, b, "prepare", req)
	if w.Code != 409 {
		t.Fatal("conflicting deployment retry accepted")
	}
}
func TestNativeListenerReservationsAndUnavailableTunnelRecovery(t *testing.T) {
	b, _, req := newNativeTest(t)
	req.Options["control_port"] = 53001
	req.Execution = prepareNativeTest(t, b, req)
	another := req
	another.DeploymentID = "deployment-2"
	another.Execution = nativeExecution{}
	_, w := nativeCall(t, b, "prepare", another)
	if w.Code != 409 {
		t.Fatal("overlapping control port accepted")
	}
	nativeCall(t, b, "start", startNativeRequest(req))
	d, _ := b.record(req.DeploymentID)
	b.tunnels.closeAll()
	occupied, e := net.Listen("tcp", fmtNativeAddr(d.LocalPort))
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	out, w := nativeCall(t, b, "inspect", req)
	if w.Code != 200 || out.Ready {
		t.Fatal("unrecoverable tunnel reported ready")
	}
}
func fmtNativeAddr(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func TestNativeArtifactUploadIntegrityAndImmutability(t *testing.T) {
	b, _, _ := newNativeTest(t)
	body := testELF()
	a := nativeArtifact{ID: "upload", AppName: "fixture-app", AppVersion: "1.0.0", Platform: "linux-amd64", Executable: "app", Files: []nativeArtifactFile{{Path: "app", SHA256: nativeHash(string(body)), Size: int64(len(body))}}}
	send := func(method, tail string, body io.Reader) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, nativePrefix+"/artifacts"+tail, body)
		b.serveHTTP(w, r)
		return w
	}
	if w := send("POST", "", strings.NewReader(nativeJSON(a))); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := send("PUT", "/upload/files/app", strings.NewReader("wrong")); w.Code != 400 {
		t.Fatal("wrong checksum accepted")
	}
	if w := send("POST", "/upload/seal", nil); w.Code != 400 {
		t.Fatal("missing executable accepted")
	}
	if w := send("PUT", "/upload/files/app", bytes.NewReader(body)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := send("POST", "/upload/seal", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := send("PUT", "/upload/files/app", bytes.NewReader(body)); w.Code != 409 {
		t.Fatal("sealed artifact changed")
	}
	a.Files[0].Path = "../escape"
	a.ID = "bad"
	if w := send("POST", "", strings.NewReader(nativeJSON(a))); w.Code != 400 {
		t.Fatal("traversal accepted")
	}
}
func TestNativeRouteIsProtectedAndDiscoverable(t *testing.T) {
	app := &App{}
	found := false
	for _, r := range app.HTTPRoutes() {
		if r.Pattern == nativePrefix+"/" {
			found = true
			if r.NoAuth {
				t.Fatal("backend route bypasses SDK authentication")
			}
		}
	}
	if !found {
		t.Fatal("route missing")
	}
	found = false
	for _, r := range app.Manifest().Provides.HTTPRoutes {
		if r.Prefix == nativePrefix {
			found = true
		}
	}
	if !found {
		t.Fatal("native backend not discoverable")
	}
}

func TestNativeSSHEncryptedStdinTransfer(t *testing.T) {
	dir := t.TempDir()
	old := dialAdministrativeSSH
	defer func() { dialAdministrativeSSH = old }()
	dialAdministrativeSSH = func(*Instance, time.Duration) (*ssh.Client, error) { return auditSSHClient(t, dir), nil }
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := nativeRunInput(c, &Instance{ID: 99}, "cat > protected-input; printf complete", strings.NewReader("sensitive-credential")); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "protected-input"))
	if e != nil || string(raw) != "sensitive-credential" {
		t.Fatal("encrypted stdin transfer failed")
	}
}
func TestNativeUploadsBeyondLegacyTransferLimit(t *testing.T) {
	b, _, _ := newNativeTest(t)
	body := append(testELF(), make([]byte, 17<<20)...)
	a := nativeArtifact{ID: "large", AppName: "fixture-app", AppVersion: "1.0.0", Platform: "linux-amd64", Executable: "app", Files: []nativeArtifactFile{{Path: "app", SHA256: nativeHash(string(body)), Size: int64(len(body))}}}
	if e := b.registerArtifact(a); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("PUT", nativePrefix+"/artifacts/large/files/app", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("large upload: %d %s", w.Code, w.Body.String())
	}
	if e := b.sealArtifact(a); e != nil {
		t.Fatal(e)
	}
}
func TestNativeHostCannotBeDestroyedWhileDeploymentRetained(t *testing.T) {
	b, _, req := newNativeTest(t)
	prepareNativeTest(t, b, req)
	d, _ := b.record(req.DeploymentID)
	if e := nativeHostRetained(b.db, d.InstanceID); e == nil {
		t.Fatal("retained host destruction permitted")
	}
	d.Execution.State = "deleted"
	b.save(d)
	if e := nativeHostRetained(b.db, d.InstanceID); e != nil {
		t.Fatal(e)
	}
}
func TestNativeUnknownAndEnvironmentMismatchFailClosed(t *testing.T) {
	b, _, req := newNativeTest(t)
	req.Execution = prepareNativeTest(t, b, req)
	start := startNativeRequest(req)
	start.Environment["APTEVA_GATEWAY_URL"] = "https://wrong-gateway.test"
	_, w := nativeCall(t, b, "start", start)
	if w.Code != 409 {
		t.Fatal("wrong callback gateway accepted")
	}
	start = startNativeRequest(req)
	start.Environment["APTEVA_RUNTIME_MODE"] = "active"
	_, w = nativeCall(t, b, "start", start)
	if w.Code != 409 {
		t.Fatal("passive app started active")
	}
	req.InstallID = 98
	_, w = nativeCall(t, b, "stop", req)
	if w.Code != 409 {
		t.Fatal("cross-install stop accepted")
	}
}

func TestNativeArtifactPublisherUsesProtectedStreamingHandoff(t *testing.T) {
	bun, e := exec.LookPath("bun")
	if e != nil {
		t.Skip("Bun is not installed")
	}
	b, _, _ := newNativeTest(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "app"), testELF(), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer publisher-test-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		b.serveHTTP(w, r)
	}))
	defer server.Close()
	run := func() []byte {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(c, bun, "run", "runtime/stage-artifact.ts", root, server.URL, "fixture-app", "1.0.0", "linux-amd64", "app")
		cmd.Env = append(os.Environ(), "APTEVA_APP_TOKEN=publisher-test-token")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("publisher: %v %s", e, out)
		}
		if bytes.Contains(out, []byte("publisher-test-token")) {
			t.Fatal("publisher exposed credential")
		}
		return out
	}
	first, second := run(), run()
	if !bytes.Equal(first, second) {
		t.Fatal("publisher retry did not reuse sealed artifact")
	}
}
func TestNativeLiveReadOnlyHostProbe(t *testing.T) {
	name := os.Getenv("INSTANCES_NATIVE_PROBE_DB")
	gateway := os.Getenv("INSTANCES_NATIVE_PROBE_GATEWAY")
	if name == "" || gateway == "" {
		t.Skip("opt-in read-only SSH validation")
	}
	db, e := sql.Open("sqlite", "file:"+name+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := strconv.ParseInt(os.Getenv("INSTANCES_NATIVE_PROBE_INSTANCE_ID"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	inst, e := dbGetInstance(db, id)
	if e != nil {
		t.Fatal(e)
	}
	if inst.IsLocal() || inst.SSHHostKey == "" {
		t.Fatal("a previously pinned remote host is required")
	}
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, e := (nativeSSH{}).Probe(c, inst, gateway)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("readonly validation: platform=%s available_memory_mb=%d available_disk_mb=%d", p.Platform, p.MemoryMB, p.DiskMB)
}
