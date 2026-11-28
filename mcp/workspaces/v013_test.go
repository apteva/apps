package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreviewProxyForwardsWithoutCredentials(t *testing.T) {
	a, ctx, one, _ := widgetFixture(t)
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Set-Cookie", "evil=1; Path=/")
		w.Header().Set("Content-Security-Policy", "default-src *")
		if r.URL.Path == "/login" {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		fmt.Fprint(w, "hello from preview")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := savePreview(ctx.AppDB(), &Preview{WorkspaceID: one.ID, ContainerPort: port, HostPort: port, Status: "live", StartedAt: nowUTC(), AccessToken: "tok123"}); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/apps/workspaces/p/" + one.ID + "/tok123"
	route := "/p/" + one.ID + "/tok123"

	rec := httptest.NewRecorder()
	a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, route+"?x=1", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != prefix+"/?x=1" {
		t.Fatalf("no-slash redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	req := httptest.NewRequest(http.MethodGet, route+"/assets/app.js?v=2", nil)
	req.Header.Set("Cookie", "apteva_session=secret")
	req.Header.Set("Authorization", "Bearer sk-secret")
	req.Header.Set("X-Apteva-Principal", "signed")
	rec = httptest.NewRecorder()
	a.handlePreviewProxy(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello from preview" {
		t.Fatalf("proxy = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Set-Cookie") != "" || len(rec.Header().Values("Content-Security-Policy")) != 1 {
		t.Fatalf("preview headers leaked: %v", rec.Header())
	}
	if got.URL.Path != "/assets/app.js" || got.URL.RawQuery != "v=2" {
		t.Fatalf("upstream path = %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Cookie") != "" || got.Header.Get("Authorization") != "" || got.Header.Get("X-Apteva-Principal") != "" {
		t.Fatalf("credentials reached the preview: %v", got.Header)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("CSP = %q", csp)
	}
	if got.Header.Get("X-Forwarded-Prefix") != prefix {
		t.Fatalf("X-Forwarded-Prefix = %q", got.Header.Get("X-Forwarded-Prefix"))
	}

	rec = httptest.NewRecorder()
	a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, route+"/login", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != prefix+"/dashboard" {
		t.Fatalf("redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	if _, err := ctx.AppDB().Exec(`UPDATE workspace_previews SET status='stopped' WHERE workspace_id=?`, one.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, route+"/", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "workspace_preview_start") {
		t.Fatalf("stopped preview = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, "/p/wsp_nope/tok123/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown workspace = %d", rec.Code)
	}
	for _, bad := range []string{"/p/" + one.ID + "/wrong/", "/p/" + one.ID + "//", "/p/" + one.ID} {
		rec = httptest.NewRecorder()
		a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// A local workspace previews on any port without preview_port at creation,
// and workspace_processes sees and stops the server.
func TestLocalPreviewOnAnyPortAndProcesses(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	t.Setenv("APTEVA_PROJECT_ID", "p")
	a, ctx, _ := localWorkspaceFixture(t, true)
	globalCtx = ctx
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "tc-create"), ctx, map[string]any{"name": "web", "driver": "local"}, false)
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	started, err := a.toolPreviewStart(agentContext("p", 5, "t1", "tc-p"), ctx, map[string]any{
		"workspace_id": w.ID, "port": port,
		"argv": []any{"python3", "-m", "http.server", strconv.Itoa(port), "--bind", "127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := started.(map[string]any)["preview"].(*Preview)
	if p.ContainerPort != port || !strings.HasPrefix(p.URL, "/api/apps/workspaces/p/"+w.ID+"/") || len(p.AccessToken) != 32 {
		t.Fatalf("preview = %+v", p)
	}
	deadline := time.Now().Add(15 * time.Second)
	for p.Status != "live" && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		got, err := a.toolPreviewGet(agentContext("p", 5, "t1", "tc-g"), ctx, map[string]any{"workspace_id": w.ID})
		if err != nil {
			t.Fatal(err)
		}
		p = got.(map[string]any)["preview"].(*Preview)
	}
	if p.Status != "live" {
		t.Fatalf("preview never live: %+v", p)
	}
	rec := httptest.NewRecorder()
	a.handlePreviewProxy(rec, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(p.URL, "/api/apps/workspaces"), nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Directory listing") {
		t.Fatalf("proxied preview = %d %.200q", rec.Code, rec.Body.String())
	}

	listed, err := a.toolProcesses(agentContext("p", 5, "t1", "tc-ps"), ctx, map[string]any{"workspace_id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(listed)
	var out struct {
		Processes []localProcess `json:"processes"`
	}
	_ = json.Unmarshal(raw, &out)
	pid := 0
	for _, proc := range out.Processes {
		if strings.Contains(proc.Command, "http.server") {
			pid = proc.PID
		}
	}
	if pid == 0 {
		t.Fatalf("server not listed: %s", raw)
	}
	if _, err := a.toolProcesses(agentContext("p", 5, "t1", "tc-k0"), ctx, map[string]any{"workspace_id": w.ID, "kill": os.Getpid()}); err == nil {
		t.Fatal("killing a process outside the workspace must be refused")
	}
	if _, err := a.toolProcesses(agentContext("p", 5, "t1", "tc-k"), ctx, map[string]any{"workspace_id": w.ID, "kill": pid}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if procExists(pid) == false {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if procExists(pid) {
		t.Fatalf("process %d still running", pid)
	}
	_, _ = a.toolDestroy(agentContext("p", 5, "t1", "tc-d"), ctx, map[string]any{"workspace_id": w.ID, "confirm": true})
}

func procExists(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	state := strings.TrimSpace(string(out))
	return err == nil && state != "" && !strings.HasPrefix(state, "Z")
}

func TestDockerPreviewWithoutPortSaysSo(t *testing.T) {
	a, _, one, _ := widgetFixture(t)
	_, err := a.toolPreviewStart(agentContext("p", 7, "thr_1", "tc-p"), globalCtx, map[string]any{"workspace_id": one.ID, "argv": []any{"true"}})
	if err == nil || !strings.Contains(err.Error(), "preview_port") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalPortSetRecordsPort(t *testing.T) {
	d := newLocalDriver(t.TempDir(), sandboxSeatbelt, "open")
	var run workloadResponse
	if err := d.Call("containers_run", map[string]any{"name": "x"}, &run); err != nil {
		t.Fatal(err)
	}
	if err := d.Call("containers_port_set", map[string]any{"workload_id": run.Workload.ID, "container_port": 5173}, &run); err != nil {
		t.Fatal(err)
	}
	if len(run.Workload.Ports) != 1 || run.Workload.Ports[0].ContainerPort != 5173 || run.Workload.Ports[0].HostPort != 5173 {
		t.Fatalf("ports = %+v", run.Workload.Ports)
	}
	if err := d.Call("containers_port_set", map[string]any{"workload_id": run.Workload.ID, "container_port": 70000}, &run); err == nil {
		t.Fatal("bad port accepted")
	}
}

func TestWorkspaceUserEntries(t *testing.T) {
	dir := t.TempDir()
	etcPasswd, etcGroup = filepath.Join(dir, "passwd"), filepath.Join(dir, "group")
	t.Cleanup(func() { etcPasswd, etcGroup = "/etc/passwd", "/etc/group" })
	_ = os.WriteFile(etcPasswd, []byte("root:x:0:0:root:/root:/bin/sh"), 0o644)
	_ = os.WriteFile(etcGroup, []byte("root:x:0:root\n"), 0o644)
	for i := 0; i < 2; i++ {
		if err := ensureWorkspaceUser(200007, "/w/.home"); err != nil {
			t.Fatal(err)
		}
	}
	_ = ensureWorkspaceUser(200008, "/v/.home")
	passwd, _ := os.ReadFile(etcPasswd)
	want := "root:x:0:0:root:/root:/bin/sh\nws200007:x:200007:200007:Apteva workspace:/w/.home:/bin/sh\nws200008:x:200008:200008:Apteva workspace:/v/.home:/bin/sh\n"
	if string(passwd) != want {
		t.Fatalf("passwd = %q", passwd)
	}
	removeWorkspaceUser(200007)
	passwd, _ = os.ReadFile(etcPasswd)
	group, _ := os.ReadFile(etcGroup)
	if strings.Contains(string(passwd), "ws200007") || !strings.Contains(string(passwd), "ws200008") || strings.Contains(string(group), "ws200007") || !strings.Contains(string(group), "ws200008:x:200008:") {
		t.Fatalf("after remove: %q %q", passwd, group)
	}
	if err := ensureWorkspaceUser(1000, "/h"); err != nil {
		t.Fatal(err)
	}
	if passwd, _ = os.ReadFile(etcPasswd); strings.Contains(string(passwd), "ws1000") {
		t.Fatal("a uid below the workspace range got an entry")
	}
}

func TestPkgServiceRefusesWithoutTokenAndBadNames(t *testing.T) {
	d := newLocalDriver(testLocalRoot(t, sandboxSeatbelt), sandboxContainer, "open")
	wl := &localWorkload{ID: localWorkloadPrefix + randomHex(12), Status: "running", Sandbox: sandboxContainer, UID: 200001, PkgToken: "tok"}
	if err := os.MkdirAll(d.workloadDir(wl.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.saveWorkload(wl); err != nil {
		t.Fatal(err)
	}
	base, err := d.pkgURL()
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, token, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		req.Header.Set("X-Apteva-Workload", wl.ID)
		req.Header.Set("X-Apteva-Pkg-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, _ := post("/add", "wrong", "nodejs"); code != http.StatusForbidden {
		t.Fatalf("wrong token = %d", code)
	}
	code, body := post("/add", "tok", "--allow-untrusted")
	if _, err := os.Stat(apkPath); err == nil {
		if code != http.StatusOK || !strings.Contains(body, "not a package name") || !strings.HasSuffix(body, "pkg-exit: 2\n") {
			t.Fatalf("flag as name = %d %q", code, body)
		}
	} else if !strings.Contains(body, "no apk") || !strings.HasSuffix(body, "pkg-exit: 1\n") {
		t.Fatalf("no apk = %d %q", code, body)
	}
	env := d.pkgEnv(wl)
	if env["APTEVA_PKG_TOKEN"] != "tok" && env["APTEVA_PKG_UNAVAILABLE"] == "" {
		t.Fatalf("env = %v", env)
	}
	d.setLimits(512, false)
	if !strings.Contains(d.packagesUnavailable(wl), "local_packages") {
		t.Fatalf("off = %q", d.packagesUnavailable(wl))
	}
	osWl := &localWorkload{ID: wl.ID, Sandbox: sandboxSeatbelt}
	if d.packagesUnavailable(osWl) == "" {
		t.Fatal("OS-sandbox workspaces cannot install system packages")
	}
}

func TestPackageName(t *testing.T) {
	for full, want := range map[string]string{"nodejs-22.11.0-r1": "nodejs", "py3-pip-24.3.1-r0": "py3-pip", "build-base-0.5-r3": "build-base"} {
		if got := packageName(full); got != want {
			t.Fatalf("%s -> %s", full, got)
		}
	}
}
