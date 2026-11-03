package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRenderer(t *testing.T) (*RendererApp, *sdk.AppCtx) {
	t.Helper()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()))
	a := &RendererApp{}
	if err := a.OnMount(app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.OnUnmount(app); err != nil {
			t.Error(err)
		}
	})
	return a, app
}
func args(id, command string, weight int) map[string]any {
	return map[string]any{"job_id": id, "command": command, "timeout_s": 10, "weight": weight, "control": weight == 0}
}
func waitJob(t *testing.T, a *RendererApp, app *sdk.AppCtx, id string) receipt {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := a.get(t.Context(), app, map[string]any{"job_id": id})
		if err != nil {
			t.Fatal(err)
		}
		r := out.(receipt)
		if terminal(r.Status) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return receipt{}
}
func activate(a *RendererApp) { a.mu.Lock(); a.active = true; a.mu.Unlock() }
func TestPassiveStartAndIdempotentExecution(t *testing.T) {
	a, app := testRenderer(t)
	request := args("once", "echo one >> count; printf result", 1)
	if _, err := a.submit(t.Context(), app, request); err == nil {
		t.Fatal("passive worker accepted processing")
	}
	activate(a)
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	r := waitJob(t, a, app, "once")
	if r.Status != "succeeded" || r.Output != "result" || !r.Stopped {
		t.Fatalf("%+v", r)
	}
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "count"))
	if err != nil || string(raw) != "one\n" {
		t.Fatalf("execution replayed: %q %v", raw, err)
	}
	if _, err := a.submit(t.Context(), app, args("once", "echo different", 1)); err == nil {
		t.Fatal("conflicting plan accepted")
	}
	if _, err := a.get(t.Context(), app.WithProject("project-b"), map[string]any{"job_id": "once"}); err == nil {
		t.Fatal("cross-project result access")
	}
}
func TestControlLaneAndConfirmedChildCancellation(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	if _, err := a.submit(t.Context(), app, args("long", "sleep 60 & echo $! > child.pid; wait", 2)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, e := os.Stat(filepath.Join(a.root, "child.pid")); e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := a.submit(t.Context(), app, args("control", "printf available", 0)); err != nil {
		t.Fatal(err)
	}
	if r := waitJob(t, a, app, "control"); r.Output != "available" {
		t.Fatalf("control lane blocked: %+v", r)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out, err := a.cancelJob(ctx, app, map[string]any{"job_id": "long"})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(receipt)
	if !r.Stopped || r.Status != "cancelled" {
		t.Fatalf("%+v", r)
	}
	if groupAlive(r.PID) {
		t.Fatal("processing descendants survived confirmed cancellation")
	}
}
func TestDeadlineStopsProcessing(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	request := args("deadline", "sleep 60", 1)
	request["timeout_s"] = 1
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	r := waitJob(t, a, app, "deadline")
	if r.Status != "cancelled" || !r.Stopped {
		t.Fatalf("%+v", r)
	}
}
func TestDrainDoesNotClaimSuccessWhileBusy(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	if _, err := a.submit(t.Context(), app, args("busy", "sleep 60", 1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_apteva/runtime/app/drain", nil).WithContext(ctx)
	a.drain(w, r)
	if w.Code == 200 {
		t.Fatal("busy worker reported drained")
	}
	if _, err := a.submit(t.Context(), app, args("new", "true", 1)); err == nil {
		t.Fatal("draining worker admitted new work")
	}
	if _, err := a.cancelJob(t.Context(), app, map[string]any{"job_id": "busy"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.drain(w, httptest.NewRequest("POST", "/_apteva/runtime/app/drain", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"drained":true`) {
		t.Fatalf("drain: %d %s", w.Code, w.Body)
	}
}
func TestRestartKeepsReceiptsAndNeverReplaysQueuedAttempt(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	if _, err := a.submit(t.Context(), app, args("finished", "printf original", 1)); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a, app, "finished")
	j := &job{receipt: receipt{JobID: "interrupted", Project: "project-a", Status: "queued", CreatedAt: time.Now()}}
	if err := a.save(j); err != nil {
		t.Fatal(err)
	}
	b := &RendererApp{}
	if err := b.OnMount(app); err != nil {
		t.Fatal(err)
	}
	defer b.OnUnmount(app)
	r := waitJob(t, b, app, "finished")
	if r.Output != "original" {
		t.Fatal("receipt lost")
	}
	r = waitJob(t, b, app, "interrupted")
	if r.Status != "failed" || !r.Stopped || !strings.Contains(r.Error, "not replayed") {
		t.Fatalf("%+v", r)
	}
}
func TestWorkerEnvironmentDoesNotExposeInstallationCredentials(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "worker-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "another-secret")
	t.Setenv("APTEVA_GATEWAY_URL", "https://private.example")
	env := strings.Join(workerEnv(t.TempDir(), "/usr/bin/ffmpeg", "/usr/bin/ffprobe", browserPaths{Chrome: "/usr/bin/chromium", Node: "/usr/bin/node"}), "\n")
	for _, secret := range []string{"worker-secret", "another-secret", "private.example", "APTEVA_APP_TOKEN"} {
		if strings.Contains(env, secret) {
			t.Fatal("credential leaked into processing environment")
		}
	}
}
func TestWorkerManifest(t *testing.T) {
	a := &RendererApp{}
	m := a.Manifest()
	if len(m.Requires.Apps) != 0 || len(m.Requires.Integrations) != 0 || len(m.Components) != 0 {
		t.Fatal("worker gained implicit dependencies")
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "instances") {
		t.Fatal("infrastructure dependency in worker")
	}
}

func TestRestartStopsKnownOrphanAndRefusesUncertainStart(t *testing.T) {
	a, app := testRenderer(t)
	cmd := exec.Command("bash", "-c", "sleep 60")
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	j := &job{receipt: receipt{JobID: "orphan", Project: "project-a", Status: "running", PID: cmd.Process.Pid, ProcessStart: processStart(cmd.Process.Pid), CreatedAt: time.Now()}}
	if err := a.save(j); err != nil {
		t.Fatal(err)
	}
	b := &RendererApp{}
	if err := b.OnMount(app); err != nil {
		t.Fatal(err)
	}
	if r := waitJob(t, b, app, "orphan"); !r.Stopped || r.Status != "failed" || groupAlive(cmd.Process.Pid) {
		t.Fatalf("orphan survived: %+v", r)
	}
	b.OnUnmount(app)
	if err := a.save(&job{receipt: receipt{JobID: "unknown", Status: "starting"}}); err != nil {
		t.Fatal(err)
	}
	if err := (&RendererApp{}).OnMount(app); err == nil {
		t.Fatal("uncertain process start was treated as confirmed termination")
	}
}

func TestAcknowledgementCompactsWithoutReplaying(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	request := args("acked", "echo once >> ack-count; printf output", 1)
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a, app, "acked")
	if _, err := a.acknowledge(t.Context(), app, map[string]any{"job_id": "acked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "ack-count"))
	if err != nil || string(raw) != "once\n" {
		t.Fatal("acknowledgement removed idempotency identity")
	}
	if r := waitJob(t, a, app, "acked"); r.Output != "" {
		t.Fatal("acknowledged output was not compacted")
	}
}

func TestManagedLifecycleIdentityAndStatus(t *testing.T) {
	t.Setenv("APTEVA_DEPLOYMENT_ID", "deployment-test")
	t.Setenv("APTEVA_DEPLOYMENT_GENERATION", "7")
	a, _ := testRenderer(t)
	check := func(state string, admitting bool) {
		t.Helper()
		w := httptest.NewRecorder()
		a.status(w, httptest.NewRequest("GET", "/_apteva/runtime/app/status", nil))
		var got struct {
			DeploymentID string `json:"deployment_id"`
			Generation   int64  `json:"generation"`
			State        string `json:"state"`
			Admitting    bool   `json:"admitting"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.DeploymentID != "deployment-test" || got.Generation != 7 || got.State != state || got.Admitting != admitting {
			t.Fatalf("status=%+v", got)
		}
	}
	check("standby", false)
	var activateHandler func(http.ResponseWriter, *http.Request)
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == "/_apteva/runtime/app/activate" {
			activateHandler = route.Handler
		}
	}
	call := func(handler func(http.ResponseWriter, *http.Request), body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
	for _, handler := range []func(http.ResponseWriter, *http.Request){activateHandler, a.drain} {
		call(handler, `{}`, 409)
		call(handler, `{"deployment_id":"deployment-test","generation":6}`, 409)
		call(handler, `{"deployment_id":"other","generation":7}`, 409)
		call(handler, `{"deployment_id":"deployment-test","generation":7} {}`, 400)
	}
	check("standby", false)
	identity := `{"deployment_id":"deployment-test","generation":7}`
	call(activateHandler, identity, 200)
	check("active", true)
	call(a.drain, identity, 200)
	check("inactive", false)
}
func TestInvalidManagedLifecycleIdentityFailsMount(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "invalid"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("APTEVA_DEPLOYMENT_ID", "deployment-test")
			t.Setenv("APTEVA_DEPLOYMENT_GENERATION", raw)
			app := tk.NewAppCtx(t, "apteva.yaml", tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()))
			if err := new(RendererApp).OnMount(app); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestProgressMarkersAreReportedWhileRunning(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	if _, err := a.submit(t.Context(), app, args("progress", "echo APTEVA_PROGRESS:0.25; sleep 0.4; printf 'APTEVA_PROGRESS:0.5\\nAPTEVA_PROGRESS:9\\n'; sleep 0.4", 1)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	seen := 0.0
	for time.Now().Before(deadline) {
		out, err := a.get(t.Context(), app, map[string]any{"job_id": "progress"})
		if err != nil {
			t.Fatal(err)
		}
		r := out.(receipt)
		if !terminal(r.Status) && r.Progress > seen {
			seen = r.Progress
		}
		if terminal(r.Status) {
			if r.Status != "succeeded" || r.Progress != 1 {
				t.Fatalf("%+v", r)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if seen != 0.5 {
		t.Fatalf("running progress = %v, want 0.5 (out-of-range markers ignored)", seen)
	}
}
func TestBrowserNeedsAreValidatedAndFailExplicitly(t *testing.T) {
	a, app := testRenderer(t)
	activate(a)
	request := args("unknown-need", "true", 1)
	request["needs"] = []string{"gpu"}
	if _, err := a.submit(t.Context(), app, request); err == nil {
		t.Fatal("unknown dependency accepted")
	}
	a.browser.chromeConfig = filepath.Join(t.TempDir(), "missing-chrome")
	request = args("browser", "echo should-not-run > ran", 2)
	request["needs"] = []string{"browser"}
	if _, err := a.submit(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	r := waitJob(t, a, app, "browser")
	if r.Status != "failed" || !r.Stopped || !strings.Contains(r.Error, "browser dependencies unavailable") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(a.root, "ran")); err == nil {
		t.Fatal("plan ran without its browser dependencies")
	}
	caps, _ := a.capabilities(t.Context(), app, nil)
	if !strings.Contains(fmt.Sprint(caps), "missing-chrome") {
		t.Fatalf("capabilities do not report the browser failure: %v", caps)
	}
}
func TestBrowserEnvironmentIsExposedToPlans(t *testing.T) {
	env := strings.Join(workerEnv("/root", "/opt/ff/ffmpeg", "/opt/ff/ffprobe", browserPaths{Chrome: "/opt/chrome", Node: "/opt/node", ExtraArgs: []string{"--no-sandbox"}}), "\n")
	for _, want := range []string{"CHROME_BIN=/opt/chrome", "NODE_BIN=/opt/node", "CHROME_EXTRA_ARGS=--no-sandbox", "FFMPEG=/opt/ff/ffmpeg", "PATH=/opt/ff:"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q in %s", want, env)
		}
	}
	if strings.Contains(strings.Join(workerEnv("/root", "/opt/ff/ffmpeg", "/opt/ff/ffprobe", browserPaths{}), "\n"), "CHROME_BIN") {
		t.Fatal("browser variables set without browser dependencies")
	}
}
func TestArchiveExtractionRejectsEscapes(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.zip")
	f, _ := os.Create(archive)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("../escape")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()
	if err := extractZip(archive, filepath.Join(dir, "out"), 1<<20); err == nil {
		t.Fatal("zip path escape accepted")
	}
	good := filepath.Join(dir, "good.zip")
	f, _ = os.Create(good)
	zw = zip.NewWriter(f)
	h := &zip.FileHeader{Name: "pkg/bin/tool"}
	h.SetMode(0o755)
	w, _ = zw.CreateHeader(h)
	w.Write([]byte("#!/bin/sh\necho ok\n"))
	zw.Close()
	f.Close()
	out := filepath.Join(dir, "good")
	if err := extractZip(good, out, 1<<20); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(out, "pkg", "bin", "tool")); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("executable bit lost: %v %v", st, err)
	}
	tgz := filepath.Join(dir, "node.tar.gz")
	f, _ = os.Create(tgz)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, name := range []string{"node-v1/README.md", "node-v1/bin/node"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 2, Typeflag: tar.TypeReg})
		tw.Write([]byte("ok"))
	}
	tw.Close()
	gw.Close()
	f.Close()
	dest := filepath.Join(dir, "node", "bin", "node")
	if err := extractTarGzMember(tgz, "node-v1/bin/node", dest); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzMember(tgz, "node-v1/bin/missing", filepath.Join(dir, "x")); err == nil {
		t.Fatal("missing member reported as extracted")
	}
}
