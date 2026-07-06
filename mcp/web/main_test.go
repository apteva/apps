package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
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

func TestParseGoogleSearchResults(t *testing.T) {
	extracted := &browserExtractResult{
		Links: []linkInfo{
			{URL: "https://www.google.com/search?q=alpha", Text: "Web"},
			{URL: "https://support.google.com/websearch/answer/181196?hl=en", Text: "Accessibility help"},
			{URL: "https://www.flexoffers.com/affiliate-programs/financial-services/peer-to-peer-lending/", Text: "Peer-To-Peer Lending Affiliate Programs | FlexOffers.com flexoffers.com https://www.flexoffers.com › financial-services"},
			{URL: "https://www.kuflink.com/affiliates/", Text: "Affiliate PartnershipsKuflinkhttps://www.kuflink.com › affiliates"},
			{URL: "https://www.kuflink.com/affiliates/#:~:text=foo", Text: "Read more"},
		},
	}
	got := parseGoogleSearch(extracted, 10)
	if len(got) != 2 {
		t.Fatalf("results len=%d, want 2: %#v", len(got), got)
	}
	if got[0].Source != "google" || got[0].URL != "https://www.flexoffers.com/affiliate-programs/financial-services/peer-to-peer-lending/" {
		t.Fatalf("first result wrong: %#v", got[0])
	}
	if got[1].URL != "https://www.kuflink.com/affiliates/" {
		t.Fatalf("second result wrong: %#v", got[1])
	}
}

func TestSearchUsesComputerDOMParser(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)

	outAny, err := app.toolSearch(ctx, map[string]any{"query": "alpha", "limit": 2, "cache": "bypass"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	out := outAny.(map[string]any)
	if got := out["extraction_backend"]; got != "browser_dom" {
		t.Fatalf("backend=%v", got)
	}
	if _, ok := out["fetch"]; ok {
		t.Fatalf("search should not expose HTTP fetch metadata: %#v", out["fetch"])
	}
	results := out["results"].([]searchResult)
	if len(results) != 2 {
		t.Fatalf("results len=%d, want 2: %#v", len(results), results)
	}
	if results[0].URL != "https://www.flexoffers.com/affiliate-programs/financial-services/peer-to-peer-lending/" || results[0].Source != "google" {
		t.Fatalf("first result wrong: %#v", results[0])
	}
	if out["engine"] != "google" {
		t.Fatalf("engine=%v, want google", out["engine"])
	}
	calls := plat.callLog()
	want := []string{"computer.browser_open", "computer.browser_extract", "computer.browser_close"}
	if !sameOrderedPrefix(calls, want) {
		t.Fatalf("calls=%v want prefix %v", calls, want)
	}
	extractArgs := plat.lastCall("computer", "browser_extract")
	formats, _ := extractArgs["formats"].([]string)
	if !sameStrings(formats, []string{"text", "html", "links", "metadata"}) {
		t.Fatalf("formats=%#v", formats)
	}
}

func TestSearchBlockedIsReportedAndNotCached(t *testing.T) {
	plat := newFakePlatform()
	plat.searchBlocked = true
	ctx, app := newTestCtx(t, plat)

	outAny, err := app.toolSearch(ctx, map[string]any{"query": "blocked", "engine": "duckduckgo", "cache": "refresh"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	out := outAny.(map[string]any)
	if out["blocked"] != true {
		t.Fatalf("blocked=%v out=%#v", out["blocked"], out)
	}
	if errText, _ := out["error"].(string); !strings.Contains(errText, "search_blocked") {
		t.Fatalf("error=%q", errText)
	}
	cache := out["cache"].(cacheInfo)
	if cache.Stored {
		t.Fatalf("blocked search should not be cached: %#v", cache)
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

func TestSmartSnapshotUsesRegionExtraction(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat, tk.WithProjectID("proj-web"))

	outAny, err := app.toolSnapshot(ctx, map[string]any{
		"url":       "https://example.com",
		"query":     "affiliate contact email",
		"max_shots": 1,
	})
	if err != nil {
		t.Fatalf("smart snapshot: %v", err)
	}
	out := outAny.(map[string]any)
	if out["mode"] != "smart" || out["storage_id"] != int64(10) {
		t.Fatalf("smart output wrong: %#v", out)
	}
	shots := out["shots"].([]map[string]any)
	if len(shots) != 1 {
		t.Fatalf("shots len=%d out=%#v", len(shots), out)
	}
	if shots[0]["region_id"] != "r_contact" || shots[0]["stored"] != true {
		t.Fatalf("shot wrong: %#v", shots[0])
	}
	calls := plat.callLog()
	want := []string{"computer.browser_open", "computer.browser_extract", "computer.computer_use", "computer.browser_screenshot", "storage.files_upload", "computer.browser_close"}
	if !sameOrderedPrefix(calls, want) {
		t.Fatalf("calls=%v want prefix %v", calls, want)
	}
	extractArgs := plat.lastCall("computer", "browser_extract")
	formats, _ := extractArgs["formats"].([]string)
	if !sameStrings(formats, []string{"regions"}) {
		t.Fatalf("formats=%#v", extractArgs["formats"])
	}
	upload := plat.lastCall("storage", "files_upload")
	if upload["content_type"] != "image/png" {
		t.Fatalf("content_type=%v", upload["content_type"])
	}
}

func TestCropScreenshotAcceptsJPEGInput(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 12, G: 34, B: 56, A: 255}}, image.Point{}, draw.Src)
	var src bytes.Buffer
	if err := jpeg.Encode(&src, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}

	cropped, w, h, err := cropScreenshotPNG(base64.StdEncoding.EncodeToString(src.Bytes()), browserRect{
		X:      10,
		Y:      10,
		Width:  40,
		Height: 30,
	}, 120, 80, 0)
	if err != nil {
		t.Fatalf("crop jpeg: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(cropped)
	if err != nil {
		t.Fatalf("decode crop: %v", err)
	}
	if w != 40 || h != 30 {
		t.Fatalf("crop size=%dx%d, want 40x30", w, h)
	}
	if string(raw[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("crop output is not PNG: % x", raw[:8])
	}
}

type fakeCall struct {
	app, tool string
	args      map[string]any
}

type fakePlatform struct {
	tk.BasePlatformClient
	mu            sync.Mutex
	calls         []fakeCall
	storageID     int64
	storageURL    string
	openURL       string
	searchBlocked bool
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
		if strings.Contains(p.openURL, "google.com/search") {
			if p.searchBlocked {
				return map[string]any{
					"session_id":         in["session_id"],
					"backend":            "local",
					"current_url":        p.openURL,
					"title":              "Google Search",
					"text":               "Our systems have detected unusual traffic from your computer network.",
					"links":              []map[string]any{{"url": "https://www.google.com/sorry/index", "text": "Google"}},
					"rendered":           true,
					"extraction_backend": "browser_dom",
				}
			}
			return map[string]any{
				"session_id":  in["session_id"],
				"backend":     "local",
				"current_url": p.openURL,
				"title":       "Google Search",
				"text":        "Peer-To-Peer Lending Affiliate Programs Affiliate Partnerships",
				"links": []map[string]any{
					{"url": "https://www.google.com/search?q=alpha", "text": "Web"},
					{"url": "https://www.flexoffers.com/affiliate-programs/financial-services/peer-to-peer-lending/", "text": "Peer-To-Peer Lending Affiliate Programs | FlexOffers.com flexoffers.com https://www.flexoffers.com › financial-services"},
					{"url": "https://www.kuflink.com/affiliates/", "text": "Affiliate PartnershipsKuflinkhttps://www.kuflink.com › affiliates"},
				},
				"rendered":           true,
				"extraction_backend": "browser_dom",
			}
		}
		if strings.Contains(p.openURL, "duckduckgo.com/html/") {
			if p.searchBlocked {
				return map[string]any{
					"session_id":         in["session_id"],
					"backend":            "local",
					"current_url":        p.openURL,
					"title":              "DuckDuckGo",
					"text":               "DuckDuckGo\n\nUnfortunately, bots use DuckDuckGo too.\n\nerror-lite@duckduckgo.com",
					"links":              []map[string]any{{"url": "https://html.duckduckgo.com/html/", "text": "DuckDuckGo"}},
					"metadata":           map[string]any{"canonical": "https://duckduckgo.com/"},
					"rendered":           true,
					"extraction_backend": "browser_dom",
				}
			}
			return map[string]any{
				"session_id":  in["session_id"],
				"backend":     "local",
				"current_url": p.openURL,
				"title":       "DuckDuckGo Search",
				"text":        "Alpha result Beta result",
				"html": `<html><body>
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">Alpha result</a>
  <a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb">Beta result</a>
</body></html>`,
				"links": []map[string]any{
					{"url": "https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa", "text": "Alpha result"},
					{"url": "https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb", "text": "Beta result"},
				},
				"rendered":           true,
				"extraction_backend": "browser_dom",
			}
		}
		return map[string]any{
			"session_id":  in["session_id"],
			"backend":     "local",
			"current_url": p.openURL,
			"url":         p.openURL,
			"title":       "Readable Page",
			"description": "A page for extraction",
			"text":        "Hello This page has useful text.",
			"markdown":    "# Hello\n\nThis page has useful text.",
			"links":       []map[string]any{{"url": p.openURL + "/next", "text": "Next page"}},
			"regions": []map[string]any{{
				"id":       "r_contact",
				"tag":      "section",
				"heading":  "Affiliate contact",
				"text":     "For affiliate contact email partners@example.com.",
				"selector": "#contact",
				"rect": map[string]any{
					"x":      80,
					"y":      1100,
					"width":  520,
					"height": 180,
				},
				"viewport_rect": map[string]any{
					"x":      80,
					"y":      1100,
					"width":  520,
					"height": 180,
				},
				"coordinate_frame": "document_css_px",
				"visible":          false,
			}},
			"metadata":           map[string]any{"description": "A page for extraction"},
			"structured_data":    map[string]any{"json_ld": []any{map[string]any{"@type": "Article", "headline": "Readable Page"}}},
			"rendered":           true,
			"extraction_backend": "browser_dom",
			"width":              1280,
			"height":             720,
		}
	case "computer.browser_close":
		return map[string]any{"closed": true}
	case "computer.computer_use":
		return map[string]any{"current_url": p.openURL, "width": 1280, "height": 720}
	case "computer.browser_screenshot":
		return map[string]any{
			"png_b64":     testPNGB64(),
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

func testPNGB64() string {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 240, G: 240, B: 240, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
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
