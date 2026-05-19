package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// TestEmbeddedManifestMatchesYAML — guard the dual-source-of-truth
// hazard between apteva.yaml (read by the platform at install) and
// manifestYAML (read by the running sidecar binary).
func TestEmbeddedManifestMatchesYAML(t *testing.T) {
	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	fromEmbed, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if fromFile.Name != fromEmbed.Name {
		t.Errorf("name drift: yaml=%q embed=%q", fromFile.Name, fromEmbed.Name)
	}
	if fromFile.Version != fromEmbed.Version {
		t.Errorf("version drift: yaml=%q embed=%q", fromFile.Version, fromEmbed.Version)
	}
	if !sameToolList(fromFile.Provides.MCPTools, fromEmbed.Provides.MCPTools) {
		t.Errorf("tool drift: yaml=%v embed=%v",
			toolNames(fromFile.Provides.MCPTools),
			toolNames(fromEmbed.Provides.MCPTools))
	}
}

// TestCapture covers the happy path: capture inserts a row, returns
// {screenshot_id, storage_id, url}; computer + storage receive
// browser_open/screenshot/close + files_upload in the right order;
// the storage upload contains the base64 bytes computer handed us
// AND lands under a dated dotted folder per the storage skill.
func TestCapture(t *testing.T) {
	plat := newFakePlatform()
	plat.computerSessionID = "br_abc"
	plat.screenshotPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0xde, 0xad, 0xbe, 0xef}
	plat.storageID = 42
	plat.storageURL = "https://storage.test/file/42"

	ctx, app := newTestCtx(t, plat)

	out, err := app.toolCapture(ctx, map[string]any{
		"url":     "https://example.com",
		"backend": "local",
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	res := out.(map[string]any)
	if res["screenshot_id"].(int64) <= 0 {
		t.Errorf("screenshot_id must be >0; got %v", res["screenshot_id"])
	}
	if res["storage_id"] != int64(42) {
		t.Errorf("storage_id: want 42, got %v", res["storage_id"])
	}
	if res["url"] != "https://storage.test/file/42" {
		t.Errorf("url passthrough wrong: got %v", res["url"])
	}

	calls := plat.callLog()
	wantOrder := []string{"computer.browser_open", "computer.browser_screenshot", "storage.files_upload", "computer.browser_close"}
	if !sameOrderedPrefix(calls, wantOrder) {
		t.Fatalf("call order:\n got %v\nwant prefix %v", calls, wantOrder)
	}

	upload := plat.lastCall("storage", "files_upload")
	if upload == nil {
		t.Fatalf("no files_upload call recorded")
	}
	gotPNG, _ := base64.StdEncoding.DecodeString(upload["content_base64"].(string))
	if string(gotPNG) != string(plat.screenshotPNG) {
		t.Errorf("uploaded bytes != screenshot bytes")
	}
	wantFolderPrefix := "/.screenshots/"
	if folder := upload["folder"].(string); !startsWith(folder, wantFolderPrefix) {
		t.Errorf("upload folder %q does not start with %q", folder, wantFolderPrefix)
	}
	if ct := upload["content_type"]; ct != "image/png" {
		t.Errorf("upload content_type: want image/png, got %v", ct)
	}
}

// TestCaptureIdempotency — same key within window returns the same
// row without re-opening Chrome.
func TestCaptureIdempotency(t *testing.T) {
	plat := newFakePlatform()
	plat.computerSessionID = "br_1"
	plat.screenshotPNG = []byte("fake-png")
	plat.storageID = 7
	plat.storageURL = "https://storage.test/file/7"

	ctx, app := newTestCtx(t, plat)

	args := map[string]any{
		"url":             "https://example.com",
		"idempotency_key": "operator-button-1",
	}

	first, err := app.toolCapture(ctx, args)
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	firstID := first.(map[string]any)["screenshot_id"].(int64)
	plat.resetLog()

	second, err := app.toolCapture(ctx, args)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	secondID := second.(map[string]any)["screenshot_id"].(int64)
	if firstID != secondID {
		t.Errorf("idempotency replay returned new id: first=%d second=%d", firstID, secondID)
	}

	// The replay path should not have opened a new browser, only
	// asked storage for a fresh signed URL.
	for _, call := range plat.callLog() {
		if call == "computer.browser_open" {
			t.Errorf("idempotency replay opened a new browser; calls=%v", plat.callLog())
		}
	}
	if !containsCall(plat.callLog(), "storage.files_get") {
		t.Errorf("replay should resolve URL via files_get; calls=%v", plat.callLog())
	}
}

// TestListGetDelete — list + get + delete round trip.
func TestListGetDelete(t *testing.T) {
	plat := newFakePlatform()
	plat.computerSessionID = "br_x"
	plat.screenshotPNG = []byte("p")
	plat.storageID = 99
	plat.storageURL = "https://storage.test/file/99"

	ctx, app := newTestCtx(t, plat)

	if _, err := app.toolCapture(ctx, map[string]any{"url": "https://a.example", "label": "alpha"}); err != nil {
		t.Fatalf("capture A: %v", err)
	}
	plat.storageID = 100
	if _, err := app.toolCapture(ctx, map[string]any{"url": "https://b.example", "label": "bravo"}); err != nil {
		t.Fatalf("capture B: %v", err)
	}

	// list
	listOut, err := app.toolList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listed := listOut.(map[string]any)["screenshots"].([]map[string]any)
	if len(listed) != 2 {
		t.Fatalf("list: want 2, got %d", len(listed))
	}

	// list with label filter
	filtered, _ := app.toolList(ctx, map[string]any{"label_contains": "alpha"})
	if got := len(filtered.(map[string]any)["screenshots"].([]map[string]any)); got != 1 {
		t.Errorf("label filter: want 1, got %d", got)
	}

	// get
	firstID := listed[0]["id"].(int64)
	getOut, err := app.toolGet(ctx, map[string]any{"screenshot_id": int(firstID)})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if getOut.(map[string]any)["url"] == "" {
		t.Errorf("get missing url")
	}

	// delete
	delOut, err := app.toolDelete(ctx, map[string]any{"screenshot_id": int(firstID)})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if delOut.(map[string]any)["deleted"] != true {
		t.Errorf("delete: want deleted=true, got %v", delOut)
	}

	// second delete idempotent
	delOut2, err := app.toolDelete(ctx, map[string]any{"screenshot_id": int(firstID)})
	if err != nil {
		t.Fatalf("delete 2nd: %v", err)
	}
	if delOut2.(map[string]any)["deleted"] != false {
		t.Errorf("2nd delete: want deleted=false, got %v", delOut2)
	}

	// list now shows 1
	listOut2, _ := app.toolList(ctx, map[string]any{})
	if got := len(listOut2.(map[string]any)["screenshots"].([]map[string]any)); got != 1 {
		t.Errorf("after delete: want 1, got %d", got)
	}
}

// TestProjectIDInjection — every cross-app call from a project-scoped
// install must include _project_id. Catches the regression that
// motivated the feedback_project_id_global_calls memory.
func TestProjectIDInjection(t *testing.T) {
	plat := newFakePlatform()
	plat.computerSessionID = "br_p"
	plat.screenshotPNG = []byte("p")
	plat.storageID = 1
	plat.storageURL = "u"

	ctx, app := newTestCtx(t, plat, tk.WithProjectID("proj-X"))

	if _, err := app.toolCapture(ctx, map[string]any{"url": "https://x.example"}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	for _, c := range plat.calls {
		if got, ok := c.args["_project_id"]; !ok || got != "proj-X" {
			t.Errorf("call %s.%s missing _project_id: args=%v", c.app, c.tool, c.args)
		}
	}
}

// ─── fake PlatformClient ───────────────────────────────────────────

type fakeCall struct {
	app, tool string
	args      map[string]any
}

type fakePlatform struct {
	tk.BasePlatformClient
	mu                sync.Mutex
	calls             []fakeCall
	computerSessionID string
	screenshotPNG     []byte
	storageID         int64
	storageURL        string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{}
}

func (p *fakePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, fakeCall{app: app, tool: tool, args: copyArgs(in)})
	p.mu.Unlock()

	resp := p.respond(app, tool)
	if resp == nil {
		return nil
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (p *fakePlatform) respond(app, tool string) map[string]any {
	switch fmt.Sprintf("%s.%s", app, tool) {
	case "computer.browser_open":
		return map[string]any{
			"session_id":  p.computerSessionID,
			"backend":     "local",
			"current_url": "https://example.com/landed",
			"width":       1024,
			"height":      768,
		}
	case "computer.browser_screenshot":
		return map[string]any{
			"png_b64":     base64.StdEncoding.EncodeToString(p.screenshotPNG),
			"current_url": "https://example.com/landed",
			"width":       1024,
			"height":      768,
		}
	case "computer.browser_close":
		return map[string]any{"closed": true}
	case "storage.files_upload":
		return map[string]any{"id": p.storageID, "url": p.storageURL}
	case "storage.files_get":
		return map[string]any{
			"file":  map[string]any{"id": p.storageID, "url": p.storageURL},
			"found": true,
		}
	case "storage.files_get_url":
		// kept for older test paths that still poke this directly
		return map[string]any{"url": p.storageURL}
	case "storage.files_delete":
		return map[string]any{}
	}
	return nil
}

func (p *fakePlatform) callLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.calls))
	for i, c := range p.calls {
		out[i] = c.app + "." + c.tool
	}
	return out
}

func (p *fakePlatform) lastCall(app, tool string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.calls) - 1; i >= 0; i-- {
		c := p.calls[i]
		if c.app == app && c.tool == tool {
			return c.args
		}
	}
	return nil
}

func (p *fakePlatform) resetLog() {
	p.mu.Lock()
	p.calls = nil
	p.mu.Unlock()
}

// ─── helpers ───────────────────────────────────────────────────────

func newTestCtx(t *testing.T, plat *fakePlatform, extra ...tk.Option) (*sdk.AppCtx, *App) {
	t.Helper()
	opts := append([]tk.Option{tk.WithPlatform(plat)}, extra...)
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	return ctx, &App{}
}

func copyArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sameOrderedPrefix(calls, want []string) bool {
	if len(calls) < len(want) {
		return false
	}
	for i, w := range want {
		if calls[i] != w {
			return false
		}
	}
	return true
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func sameToolList(a, b []sdk.MCPToolSpec) bool {
	an, bn := toolNames(a), toolNames(b)
	if len(an) != len(bn) {
		return false
	}
	for i := range an {
		if an[i] != bn[i] {
			return false
		}
	}
	return true
}
