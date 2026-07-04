package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestEmbeddedManifestMatchesYAML(t *testing.T) {
	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse yaml manifest: %v", err)
	}
	fromEmbed, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if fromFile.Name != fromEmbed.Name {
		t.Fatalf("name drift: file=%q embed=%q", fromFile.Name, fromEmbed.Name)
	}
	if fromFile.Version != fromEmbed.Version {
		t.Fatalf("version drift: file=%q embed=%q", fromFile.Version, fromEmbed.Version)
	}
	if !sameStrings(toolNames(fromFile.Provides.MCPTools), toolNames(fromEmbed.Provides.MCPTools)) {
		t.Fatalf("tool drift: file=%v embed=%v", toolNames(fromFile.Provides.MCPTools), toolNames(fromEmbed.Provides.MCPTools))
	}
}

func TestParseDuckDuckGoResults(t *testing.T) {
	body := []byte(`
<html><body>
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">Alpha result</a>
  <a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb">Beta result</a>
  <a href="/settings">settings</a>
</body></html>`)
	got := parseDuckDuckGo(body, 10)
	if len(got) != 2 {
		t.Fatalf("results len=%d, want 2: %#v", len(got), got)
	}
	if got[0].URL != "https://example.com/a" || got[0].Rank != 1 {
		t.Fatalf("first result wrong: %#v", got[0])
	}
	if got[1].Title != "Beta result" || got[1].Source != "duckduckgo" {
		t.Fatalf("second result wrong: %#v", got[1])
	}
}

func TestExtractURLUsesComputerDOMParser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Readable Page</title><meta name="description" content="A page for extraction"></head><body><h1>Hello</h1><p>This page has useful text.</p><a href="/next">Next page</a></body></html>`)
	}))
	defer srv.Close()

	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)

	out, err := app.toolExtract(ctx, map[string]any{"url": srv.URL, "store": false})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	page := out.(map[string]any)["page"].(pageDoc)
	if page.Title != "Readable Page" {
		t.Fatalf("title=%q", page.Title)
	}
	if page.ExtractionBackend != "browser_dom" {
		t.Fatalf("backend=%q", page.ExtractionBackend)
	}
	if len(page.Links) != 1 || page.Links[0].URL != srv.URL+"/next" {
		t.Fatalf("links=%#v", page.Links)
	}
	if page.Markdown != "# Hello\n\nThis page has useful text." {
		t.Fatalf("markdown=%q", page.Markdown)
	}
	if got := page.StructuredData["json_ld"]; got == nil {
		t.Fatalf("structured_data missing json_ld: %#v", page.StructuredData)
	}
	extractArgs := plat.lastCall("computer", "browser_extract")
	formats, _ := extractArgs["formats"].([]string)
	if len(formats) != 6 || formats[0] != "text" || formats[3] != "structured_data" {
		t.Fatalf("formats=%#v", extractArgs["formats"])
	}
	calls := plat.callLog()
	want := []string{"computer.browser_open", "computer.browser_extract", "computer.browser_close"}
	if !sameOrderedPrefix(calls, want) {
		t.Fatalf("calls=%v want prefix %v", calls, want)
	}
}

func TestExtractUsesResponseCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Cached Page</title></head><body><h1>Cached</h1></body></html>`)
	}))
	defer srv.Close()

	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	args := map[string]any{"url": srv.URL, "store": false, "max_age": 3600}

	first, err := app.toolExtract(ctx, args)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	firstCache := first.(map[string]any)["cache"].(cacheInfo)
	if firstCache.Hit || !firstCache.Stored {
		t.Fatalf("first cache=%#v, want miss stored", firstCache)
	}
	if got := len(plat.callLog()); got != 3 {
		t.Fatalf("first call count=%d, want 3", got)
	}

	second, err := app.toolExtract(ctx, args)
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	secondCache := second.(map[string]any)["cache"].(cacheInfo)
	if !secondCache.Hit || secondCache.AgeSeconds < 0 {
		t.Fatalf("second cache=%#v, want hit", secondCache)
	}
	if got := len(plat.callLog()); got != 3 {
		t.Fatalf("second call count=%d, want still 3 (cache hit)", got)
	}

	_, err = app.toolExtract(ctx, map[string]any{"url": srv.URL, "store": false, "cache": "bypass", "max_age": 3600})
	if err != nil {
		t.Fatalf("bypass extract: %v", err)
	}
	if got := len(plat.callLog()); got != 6 {
		t.Fatalf("bypass call count=%d, want 6", got)
	}
}

func TestSnapshotUploadsAndInjectsProjectID(t *testing.T) {
	plat := newFakePlatform()
	plat.storageID = 88
	plat.storageURL = "https://storage.test/signed/88"
	ctx, app := newTestCtx(t, plat, tk.WithProjectID("proj-web"))

	out, err := app.toolSnapshot(ctx, map[string]any{"url": "https://example.com", "label": "Example"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	res := out.(map[string]any)
	if res["storage_id"] != int64(88) || res["url"] != plat.storageURL {
		t.Fatalf("storage fields wrong: %#v", res)
	}
	for _, c := range plat.calls {
		if got := c.args["_project_id"]; got != "proj-web" {
			t.Fatalf("%s.%s _project_id=%v args=%v", c.app, c.tool, got, c.args)
		}
	}
	upload := plat.lastCall("storage", "files_upload")
	if upload == nil {
		t.Fatalf("missing storage upload")
	}
	if upload["content_type"] != "image/png" {
		t.Fatalf("content_type=%v", upload["content_type"])
	}
	if _, err := base64.StdEncoding.DecodeString(upload["content_base64"].(string)); err != nil {
		t.Fatalf("bad upload base64: %v", err)
	}
}

type fakeCall struct {
	app, tool string
	args      map[string]any
}

type fakePlatform struct {
	tk.BasePlatformClient
	mu         sync.Mutex
	calls      []fakeCall
	storageID  int64
	storageURL string
	openURL    string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{storageID: 10, storageURL: "https://storage.test/signed/10"}
}

func (p *fakePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, fakeCall{app: app, tool: tool, args: copyArgs(in)})
	p.mu.Unlock()
	resp := p.respond(app, tool, in)
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (p *fakePlatform) respond(app, tool string, in map[string]any) map[string]any {
	switch app + "." + tool {
	case "computer.browser_open":
		if u, ok := in["url"].(string); ok {
			p.openURL = u
		}
		return map[string]any{
			"session_id":  "sess_1",
			"backend":     "local",
			"current_url": in["url"],
			"width":       1280,
			"height":      720,
		}
	case "computer.browser_extract":
		return map[string]any{
			"session_id":         in["session_id"],
			"backend":            "local",
			"current_url":        p.openURL,
			"url":                p.openURL,
			"title":              "Readable Page",
			"description":        "A page for extraction",
			"text":               "Hello This page has useful text.",
			"markdown":           "# Hello\n\nThis page has useful text.",
			"links":              []map[string]any{{"url": p.openURL + "/next", "text": "Next page"}},
			"metadata":           map[string]any{"description": "A page for extraction"},
			"structured_data":    map[string]any{"json_ld": []any{map[string]any{"@type": "Article", "headline": "Readable Page"}}},
			"rendered":           true,
			"extraction_backend": "browser_dom",
			"width":              1280,
			"height":             720,
		}
	case "computer.browser_close":
		return map[string]any{"closed": true}
	case "computer.browser_screenshot":
		return map[string]any{
			"png_b64":     base64.StdEncoding.EncodeToString([]byte("fake-png")),
			"current_url": "https://example.com",
			"width":       1280,
			"height":      720,
		}
	case "storage.files_upload":
		return map[string]any{"id": p.storageID, "url": p.storageURL}
	default:
		return map[string]any{}
	}
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

func newTestCtx(t *testing.T, plat *fakePlatform, extra ...tk.Option) (*sdk.AppCtx, *App) {
	t.Helper()
	opts := append([]tk.Option{tk.WithPlatform(plat)}, extra...)
	return tk.NewAppCtx(t, "apteva.yaml", opts...), &App{}
}

func copyArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameOrderedPrefix(calls, want []string) bool {
	if len(calls) < len(want) {
		return false
	}
	for i := range want {
		if calls[i] != want[i] {
			return false
		}
	}
	return true
}
