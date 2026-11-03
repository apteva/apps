package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// rendererTestPlatform stands in for Server: "component:renderer" calls run
// the submitted bash plan for real (like the worker does), and one HTTP server
// plays the gateway, the relay route and the Storage callback proxy.
type rendererTestPlatform struct {
	tk.BasePlatformClient
	t       *testing.T
	gateway string // what the worker reports; differs from the minted URL host
	minted  string // host Storage mints signed URLs with
	env     []string

	mu       sync.Mutex
	submits  int
	plans    []string
	receipts map[string]rendererReceipt
	uploaded bytes.Buffer
	auth     []string
}

func newRendererTestPlatform(t *testing.T) (*rendererTestPlatform, *sdk.AppCtx) {
	t.Helper()
	p := &rendererTestPlatform{t: t, receipts: map[string]rendererReceipt{}}
	app := &App{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/apps/composer/_install/7/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/composer/_install/7")
			app.handleRendererTransfer(w, r)
		case strings.HasPrefix(r.URL.Path, composerBoundStorageProxyPath+"/"):
			p.storage(w, r)
		case strings.HasPrefix(r.URL.Path, "/fixtures/"):
			http.ServeFile(w, r, filepath.Join(os.Getenv("COMPOSER_TEST_FIXTURES"), filepath.Base(r.URL.Path)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	p.minted = server.URL // http://127.0.0.1:port
	p.gateway = strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	t.Setenv("APTEVA_GATEWAY_URL", p.minted)
	t.Setenv("APTEVA_INSTALL_ID", "7")
	t.Setenv("APTEVA_APP_TOKEN", "parent-platform-token")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p), tk.WithConfig(map[string]string{"execution_mode": "component"}))
	prior := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = prior })
	return p, ctx.WithProject("owner")
}

// storage emulates a disk-backed Storage proxy: no presigned upload, chunked
// /uploads instead.
func (p *rendererTestPlatform) storage(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.auth = append(p.auth, r.Header.Get("Authorization"))
	if r.URL.Query().Get("project_id") != "owner" {
		http.Error(w, "wrong project", 403)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, composerBoundStorageProxyPath)
	switch {
	case tail == "/files/init":
		http.Error(w, "direct upload unsupported", http.StatusNotImplemented)
	case tail == "/uploads":
		_, _ = io.WriteString(w, `{"upload_id":"up1","part_size":65536}`)
	case strings.HasPrefix(tail, "/uploads/up1/parts/"):
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			http.Error(w, "relay must decode parts", 400)
			return
		}
		_, _ = io.Copy(&p.uploaded, r.Body)
	case tail == "/uploads/up1/complete":
		_, _ = io.WriteString(w, `{"file":{"id":4242}}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+tail, 400)
	}
}

func (p *rendererTestPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	return p.CallAppResultContext(context.Background(), app, tool, args, out)
}
func (p *rendererTestPlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, errors.New("raw app calls are not used")
}
func (p *rendererTestPlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, errors.New("batch app calls are not used")
}
func (p *rendererTestPlatform) CallAppResultContext(ctx context.Context, app, tool string, args map[string]any, out any) error {
	var result any
	switch app + "." + tool {
	case "storage.files_get_url":
		result = map[string]any{"url": fmt.Sprintf("%s/fixtures/%v.bin?sig=abc", p.minted, args["id"])}
	case "component:renderer.capabilities":
		result = map[string]any{"protocol": rendererProtocol, "gateway_url": p.gateway, "platform": "test", "capacity": 2}
	case "component:renderer.submit":
		result = p.submit(ctx, args)
	case "component:renderer.get", "component:renderer.cancel":
		p.mu.Lock()
		result = p.receipts[args["job_id"].(string)]
		p.mu.Unlock()
	case "component:renderer.acknowledge":
		result = map[string]any{"acknowledged": true}
	default:
		return fmt.Errorf("unexpected call %s.%s", app, tool)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

func (p *rendererTestPlatform) submit(ctx context.Context, args map[string]any) rendererReceipt {
	id := args["job_id"].(string)
	command := args["command"].(string)
	p.mu.Lock()
	if r, ok := p.receipts[id]; ok {
		p.mu.Unlock()
		return r
	}
	p.submits++
	p.plans = append(p.plans, command)
	p.mu.Unlock()
	root := p.t.TempDir()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = root
	cmd.Env = append([]string{"HOME=" + root, "TMPDIR=" + root, "PATH=" + filepath.Dir(ffmpegPath()) + ":" + os.Getenv("PATH"), "FFPROBE=" + ffprobePath()}, p.env...)
	output, err := cmd.CombinedOutput()
	r := rendererReceipt{JobID: id, Status: "succeeded", Output: string(output), Exit: 0, Stopped: true, Progress: 1}
	if err != nil {
		r.Status, r.Error, r.Exit = "failed", "render failed", cmd.ProcessState.ExitCode()
	}
	p.mu.Lock()
	p.receipts[id] = r
	p.mu.Unlock()
	return r
}

func writeRendererFixture(t *testing.T, name string, args ...string) {
	t.Helper()
	dir := os.Getenv("COMPOSER_TEST_FIXTURES")
	full := append([]string{"-y", "-v", "error"}, args...)
	full = append(full, "-f", "mp4", filepath.Join(dir, name))
	if out, err := exec.Command(ffmpegPath(), full...).CombinedOutput(); err != nil {
		t.Fatalf("fixture %s: %v %s", name, err, out)
	}
}

func requireRendererTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{ffmpegPath(), ffprobePath(), "curl", "bash"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not available")
		}
	}
	t.Setenv("COMPOSER_TEST_FIXTURES", t.TempDir())
}

func (p *rendererTestPlatform) checkUpload(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, auth := range p.auth {
		if auth != "Bearer parent-platform-token" {
			t.Fatalf("Storage saw %q; the relay must forward with Composer's own token", auth)
		}
	}
	for _, plan := range p.plans {
		if strings.Contains(plan, "parent-platform-token") {
			t.Fatal("plan carries Composer's platform token")
		}
		if strings.Contains(plan, p.minted+"/fixtures/") || strings.Contains(plan, "/fixtures/") && !strings.Contains(plan, p.gateway+"/fixtures/") {
			t.Fatal("signed source URLs were not rebased onto the worker's gateway")
		}
	}
	out := filepath.Join(t.TempDir(), "uploaded.mp4")
	if err := os.WriteFile(out, p.uploaded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := probeRenderDuration(out); d <= 0 {
		t.Fatalf("uploaded output is not playable media (%d bytes)", p.uploaded.Len())
	}
}

func TestComponentFFmpegRenderUploadsThroughRelay(t *testing.T) {
	requireRendererTools(t)
	writeRendererFixture(t, "5.bin", "-f", "lavfi", "-i", "color=c=blue:s=320x180:d=1:r=24", "-f", "lavfi", "-i", "sine=f=440:d=1", "-shortest")
	p, app := newRendererTestPlatform(t)
	edit, err := parseEditJSON(`{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:5"},"length":1}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withComponentOwner(context.Background(), "render:1")
	exec, err := chooseExecutor(app, "")
	if err != nil || exec.Name() != "component" {
		t.Fatalf("execution_mode=component selected %v %v", exec, err)
	}
	result, err := exec.Render(ctx, app, edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := componentStorageResult(result.LocalPath); !ok || id != 4242 {
		t.Fatalf("result = %+v", result)
	}
	p.checkUpload(t)
	var grants int
	_ = app.AppDB().QueryRow(`SELECT COUNT(*) FROM renderer_grants`).Scan(&grants)
	if grants != 0 {
		t.Fatal("transfer grant outlived the persisted receipt")
	}
	// A resumed render re-attaches to the recorded receipt; it never re-executes.
	if _, err := exec.Render(ctx, app, edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, "owner"); err != nil {
		t.Fatal(err)
	}
	if p.submits != 1 {
		t.Fatalf("owner re-run submitted %d plans", p.submits)
	}
}

func TestComponentModeFailsExplicitlyWithoutRenderer(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"render_host_id": "3"})).WithProject("owner")
	if !componentRenderingEnabled(ctx) {
		t.Fatal("a legacy render_host_id must keep offloading instead of rendering locally")
	}
	edit, _ := parseEditJSON(`{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"image","src":"https://example.com/a.png"},"length":1}]}]}}`)
	_, err := (&componentFFmpegExecutor{}).Render(context.Background(), ctx, edit, Output{Format: "mp4", Resolution: "sd", Aspect: "16:9", FPS: 24}, "owner")
	if err == nil || !strings.Contains(err.Error(), "renderer component unavailable") {
		t.Fatalf("err = %v", err)
	}
	local := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"render_host_id": "3", "execution_mode": "local"}))
	if componentRenderingEnabled(local) {
		t.Fatal("explicit execution_mode=local must win over a legacy host")
	}
}

func TestRelayRejectsForeignFoldersAndExpiredGrants(t *testing.T) {
	_, app := newRendererTestPlatform(t)
	grant, _, err := newRendererGrant(app, "owner", 60)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/renderer-io"+composerBoundStorageProxyPath+"/uploads", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		(&App{}).handleRendererTransfer(w, r)
		return w.Code
	}
	if code := call(grant, `{"folder":"/private/","size":10}`); code != 403 {
		t.Fatalf("foreign folder: %d", code)
	}
	if code := call("not-a-grant", `{"folder":"/.composer/","size":10}`); code != 403 {
		t.Fatalf("unknown grant: %d", code)
	}
	if code := call(grant, `{"folder":"/.composer/","size":10}`); code != 200 {
		t.Fatalf("scoped upload init: %d", code)
	}
	_, _ = app.AppDB().Exec(`UPDATE renderer_grants SET expires_at=0`)
	if code := call(grant, `{"folder":"/.composer/","size":10}`); code != 403 {
		t.Fatalf("expired grant: %d", code)
	}
}

func browserRendererEnv(t *testing.T, p *rendererTestPlatform) {
	t.Helper()
	chrome, err := chromeExecutable()
	if err != nil {
		t.Skip("Chrome not available")
	}
	node, err := exec.LookPath(browserNodePath())
	if err != nil {
		t.Skip("node not available")
	}
	p.env = []string{"CHROME_BIN=" + chrome, "NODE_BIN=" + node}
}

func TestComponentBrowserRenderCapturesOnWorker(t *testing.T) {
	requireRendererTools(t)
	writeRendererFixture(t, "8.bin", "-f", "lavfi", "-i", "sine=f=330:d=1")
	p, app := newRendererTestPlatform(t)
	browserRendererEnv(t, p)
	spec, err := parseV2CompositionJSON(`{"version":"composer/v2","output":{"width":320,"height":180,"fps":24,"format":"mp4","renderer":"browser"},
		"assets":[{"id":"music","type":"audio","src":"storage:8"}],
		"audio":[{"id":"a1","asset":"music","start":0,"duration":1}],
		"scenes":[{"id":"s1","duration":1,"background":"#123","elements":[{"id":"t","type":"text","text":"Renderer","x":20,"y":20,"width":280,"height":80,"style":{"color":"#fff","font_size":32}}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := renderV2BrowserWindow(withComponentOwner(context.Background(), "render:2"), app, spec, "owner", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := componentStorageResult(result.LocalPath); !ok || id != 4242 {
		t.Fatalf("result = %+v", result)
	}
	p.checkUpload(t)
	if !strings.Contains(p.receipts[p.firstJob()].Output, "APTEVA_PROGRESS:0.850") {
		t.Fatal("capture did not report progress markers")
	}
}

func (p *rendererTestPlatform) firstJob() string {
	for id := range p.receipts {
		return id
	}
	return ""
}

func TestComponentWebCanvasProcedureReturnsStorageArtifact(t *testing.T) {
	requireRendererTools(t)
	p, app := newRendererTestPlatform(t)
	browserRendererEnv(t, p)
	output := defaultOutput()
	output.Resolution, output.Aspect = "sd", "16:9"
	result, err := runProcedureComponent(withComponentOwner(context.Background(), "render:3"), app, webCanvasTestRevision(), &ProceduralAsset{}, output, procedureTimeline{JobID: "composition-1:clip", Duration: 1, FPS: 12})
	if err != nil {
		t.Fatalf("%v\nlogs=%s", err, result.Logs)
	}
	if result.StorageID != 4242 || result.Kind != "video" || result.DurationSeconds < 0.9 || result.DurationSeconds > 1.1 {
		t.Fatalf("result = %+v", result)
	}
	p.checkUpload(t)
}

// Server builds the worker from the pinned commit and requires its apteva.yaml
// to equal the nested declaration except for the source ref.
func TestRendererDeclarationMatchesWorkerManifest(t *testing.T) {
	worker, err := sdk.ParseManifest(mustRead(t, "renderer/apteva.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"apteva.yaml": mustRead(t, "apteva.yaml"), "main.go manifestYAML": []byte(manifestYAML)} {
		parent, err := sdk.ParseManifest(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		decl, ok := parent.Components[rendererComponent]
		if !ok || decl.Protocol != rendererProtocol {
			t.Fatalf("%s: renderer component not declared with %s", name, rendererProtocol)
		}
		nested := decl.Manifest
		if nested.Runtime.Source == nil || len(nested.Runtime.Source.Ref) != 40 {
			t.Fatalf("%s: renderer must be pinned to a commit", name)
		}
		nested.Runtime.Source.Ref = worker.Runtime.Source.Ref
		a, _ := json.Marshal(nested)
		b, _ := json.Marshal(worker)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: nested renderer manifest drifted from renderer/apteva.yaml\n%s\n%s", name, a, b)
		}
		for _, dep := range parent.Requires.Apps {
			if dep.Name == "instances" {
				t.Fatalf("%s still depends on the Instances app", name)
			}
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
