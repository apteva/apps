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
	"time"

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

func TestInvalidSearchEngineDoesNotLeaveRunningRun(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	if _, err := app.toolSearch(ctx, map[string]any{"query": "alpha", "engine": "invalid"}); err == nil {
		t.Fatal("expected unsupported engine error")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM web_runs`).Scan(&count); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid request created %d run rows", count)
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
	if page.Status != http.StatusOK || page.ContentType != "text/html" {
		t.Fatalf("rendered page metadata status=%d content_type=%q", page.Status, page.ContentType)
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

func TestCachedExtractStillHonorsStoreRequest(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	baseArgs := map[string]any{"url": "https://example.com/cache-store", "store": false, "max_age": 3600}

	if _, err := app.toolExtract(ctx, baseArgs); err != nil {
		t.Fatalf("prime extract cache: %v", err)
	}
	outAny, err := app.toolExtract(ctx, map[string]any{"url": baseArgs["url"], "store": true, "max_age": 3600})
	if err != nil {
		t.Fatalf("cached stored extract: %v", err)
	}
	out := outAny.(map[string]any)
	if !out["cache"].(cacheInfo).Hit {
		t.Fatalf("expected cache hit: %#v", out["cache"])
	}
	page := out["page"].(pageDoc)
	if page.Artifact == nil || page.Artifact.StorageID == 0 {
		t.Fatalf("store=true did not create artifact from cached content: %#v", page)
	}
	if got := countCalls(plat, "storage", "files_upload"); got != 1 {
		t.Fatalf("storage uploads=%d, want 1", got)
	}
	if got := countCalls(plat, "computer", "browser_open"); got != 1 {
		t.Fatalf("browser opens=%d, want cache hit without second open", got)
	}
}

func TestCachedExtractDoesNotLeakArtifactToStoreFalse(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	url := "https://example.com/no-artifact-leak"
	if _, err := app.toolExtract(ctx, map[string]any{"url": url, "store": true, "max_age": 3600}); err != nil {
		t.Fatalf("stored extract: %v", err)
	}
	outAny, err := app.toolExtract(ctx, map[string]any{"url": url, "store": false, "max_age": 3600})
	if err != nil {
		t.Fatalf("unstored cached extract: %v", err)
	}
	page := outAny.(map[string]any)["page"].(pageDoc)
	if page.Artifact != nil {
		t.Fatalf("store=false leaked cached artifact: %#v", page.Artifact)
	}
}

func TestSearchVisitTopExtractsLeadingResult(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	outAny, err := app.toolSearch(ctx, map[string]any{"query": "peer lending", "visit_top": true, "cache": "bypass"})
	if err != nil {
		t.Fatalf("search visit_top: %v", err)
	}
	out := outAny.(map[string]any)
	top, ok := out["top_page"].(pageDoc)
	if !ok || top.URL == "" || top.Title != "Readable Page" {
		t.Fatalf("top_page=%#v", out["top_page"])
	}
	results := out["results"].([]searchResult)
	if len(results) == 0 || results[0].Snippet == "" {
		t.Fatalf("leading result snippet not enriched: %#v", results)
	}
	if got := countCalls(plat, "computer", "browser_open"); got != 2 {
		t.Fatalf("browser opens=%d, want search page plus top result", got)
	}
}

func TestCrawlUsesConfiguredPageLimit(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat, tk.WithConfig(map[string]string{"max_pages": "2"}))
	outAny, err := app.toolCrawl(ctx, map[string]any{"url": "https://example.com/start", "store": false, "cache": "bypass", "max_depth": 4})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	out := outAny.(map[string]any)
	if out["count"] != 2 {
		t.Fatalf("count=%v, want configured default limit 2", out["count"])
	}
}

func TestCrawlDoesNotLetDuplicateLinksStarveUniquePages(t *testing.T) {
	plat := newFakePlatform()
	plat.duplicateCrawlLinks = true
	ctx, app := newTestCtx(t, plat)
	outAny, err := app.toolCrawl(ctx, map[string]any{"url": "https://example.com/start", "max_pages": 3, "max_depth": 2, "store": false, "cache": "bypass"})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	out := outAny.(map[string]any)
	pages := out["pages"].([]pageDoc)
	if len(pages) != 3 {
		t.Fatalf("pages=%d, want start and two unique children: %#v", len(pages), pages)
	}
	seen := map[string]bool{}
	for _, page := range pages {
		seen[page.URL] = true
	}
	if !seen["https://example.com/a"] || !seen["https://example.com/b"] {
		t.Fatalf("unique pages were starved: %#v", seen)
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
	want := []string{"computer.browser_open", "computer.browser_extract", "computer.browser_extract", "computer.computer_use", "computer.browser_extract", "computer.browser_screenshot", "storage.files_upload", "computer.browser_close"}
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

func TestSmartSnapshotDismissesCookieBanner(t *testing.T) {
	plat := newFakePlatform()
	plat.cookieBanner = true
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
	cookie := out["cookie_handling"].(map[string]any)
	if cookie["strategy"] != "onetrust_coordinate_fallback" || cookie["dismissed"] != true {
		t.Fatalf("cookie_handling=%#v", cookie)
	}
	var cookieClick map[string]any
	for _, c := range plat.calls {
		if c.app == "computer" && c.tool == "computer_use" && c.args["action"] == "click" {
			cookieClick = c.args
			break
		}
	}
	if cookieClick == nil || cookieClick["coordinate"] == "" {
		t.Fatalf("missing cookie coordinate click; calls=%#v", plat.calls)
	}
}

func TestSmartSnapshotPrefersSOMTargetInsideRecognizedCookieBanner(t *testing.T) {
	plat := newFakePlatform()
	plat.cookieBanner = true
	plat.cookieBannerSOM = true
	ctx, app := newTestCtx(t, plat, tk.WithProjectID("proj-web"))

	outAny, err := app.toolSnapshot(ctx, map[string]any{"url": "https://example.com", "query": "affiliate contact", "max_shots": 1})
	if err != nil {
		t.Fatalf("smart snapshot: %v", err)
	}
	cookie := outAny.(map[string]any)["cookie_handling"].(map[string]any)
	if cookie["strategy"] != "som_accept_button" || cookie["label"] != 7 || cookie["dismissed"] != true {
		t.Fatalf("cookie_handling=%#v", cookie)
	}
	for _, call := range plat.callsSnapshot() {
		if call.app == "computer" && call.tool == "computer_use" && call.args["action"] == "click" && call.args["coordinate"] != nil {
			t.Fatalf("recognized banner used coordinate despite SoM target: %#v", call.args)
		}
	}
}

func TestSmartSnapshotDismissesCookieBannerWithSOM(t *testing.T) {
	plat := newFakePlatform()
	plat.cookieTextBanner = true
	plat.cookiePolicyText = true
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
	cookie := out["cookie_handling"].(map[string]any)
	if cookie["strategy"] != "som_accept_button" || cookie["dismissed"] != true || cookie["label"] != 9 {
		t.Fatalf("cookie_handling=%#v", cookie)
	}
	var somCalls int
	var sawLabelClick bool
	for _, c := range plat.calls {
		if c.app != "computer" || c.tool != "computer_use" {
			continue
		}
		if c.args["action"] == "screenshot" && c.args["include_som"] == true {
			somCalls++
		}
		if c.args["action"] == "click" && c.args["label"] == 9 {
			sawLabelClick = true
		}
	}
	if somCalls < 2 || !sawLabelClick {
		t.Fatalf("missing som screenshot or label click; calls=%#v", plat.calls)
	}
}

func TestRankRegionsPrefersSpecificChildOverGiantContainer(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "main",
			Tag:      "main",
			Heading:  "Build wealth with confidence",
			Text:     "Loans Real estate Bonds Smart Cash ETFs investment returns " + strings.Repeat("site navigation ", 40),
			Selector: "main",
			Rect:     browserRect{X: 0, Y: 0, Width: 1350, Height: 9480},
		},
		{
			ID:       "products",
			Tag:      "section",
			Heading:  "Loans",
			Text:     "Loans can offer competitive investment returns. Explore bonds ETFs and real estate investment products.",
			Selector: "main > section:nth-of-type(4)",
			Rect:     browserRect{X: 0, Y: 2400, Width: 1350, Height: 540},
		},
		{
			ID:       "testimonials",
			Tag:      "section",
			Heading:  "Trusted by 700k+ registered users",
			Text:     "Best investment platform with ETFs, smart cash, loans and returns.",
			Selector: "main > section:nth-of-type(10)",
			Rect:     browserRect{X: 0, Y: 6200, Width: 1350, Height: 548},
		},
	}

	got := rankRegions(regions, "investment products returns loans ETFs bonds", 3)
	if len(got) != 2 {
		t.Fatalf("ranked len=%d, want only specific sections: %#v", len(got), got)
	}
	if got[0].Region.ID == "main" || got[1].Region.ID == "main" {
		t.Fatalf("giant container should be suppressed: %#v", got)
	}
	if got[0].Region.ID != "products" {
		t.Fatalf("first region=%s, want products; ranked=%#v", got[0].Region.ID, got)
	}
}

func TestRankRegionsDedupesNestedParent(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "parent",
			Tag:      "section",
			Heading:  "Investment products",
			Text:     "Investment products include loans bonds and ETFs.",
			Selector: "section.products",
			Rect:     browserRect{X: 0, Y: 1000, Width: 1200, Height: 900},
		},
		{
			ID:       "card",
			Tag:      "section",
			Heading:  "Loans",
			Text:     "Loans can offer competitive investment returns.",
			Selector: "section.products .card",
			Rect:     browserRect{X: 80, Y: 1120, Width: 420, Height: 260},
		},
	}

	got := rankRegions(regions, "investment returns loans", 2)
	if len(got) != 1 {
		t.Fatalf("ranked len=%d, want deduped child only: %#v", len(got), got)
	}
	if got[0].Region.ID != "card" {
		t.Fatalf("region=%s, want card; ranked=%#v", got[0].Region.ID, got)
	}
}

func TestRankRegionsDedupesExactDuplicateRegions(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "hero_a",
			Tag:      "section",
			Heading:  "Invest in real estate with confidence",
			Text:     "Discover a platform for investing through property-backed loans.",
			Selector: "section.hero",
			Rect:     browserRect{X: 0, Y: 72, Width: 1350, Height: 512},
		},
		{
			ID:       "hero_b",
			Tag:      "section",
			Heading:  "Invest in real estate with confidence",
			Text:     "Discover a platform for investing through property-backed loans.",
			Selector: "section.hero",
			Rect:     browserRect{X: 0, Y: 72, Width: 1350, Height: 512},
		},
		{
			ID:       "loans",
			Tag:      "section",
			Heading:  "Loans backed by real estate",
			Text:     "Investors can review secured loans and expected returns.",
			Selector: "section.loans",
			Rect:     browserRect{X: 0, Y: 1300, Width: 1350, Height: 480},
		},
	}

	got := rankRegions(regions, "real estate platform loans investors", 3)
	if len(got) != 2 {
		t.Fatalf("ranked len=%d, want duplicate removed: %#v", len(got), got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Region.Selector] {
			t.Fatalf("duplicate selector kept: %#v", got)
		}
		seen[r.Region.Selector] = true
	}
}

func TestRankRegionsDedupesNearIdenticalNestedRegions(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "outer",
			Tag:      "section",
			Heading:  "Mortgage-backed investments",
			Text:     "Mortgage-backed investments. All loans are backed by real estate and secured with a mortgage in favor of investors.",
			Selector: "section.outer",
			Rect:     browserRect{X: 0, Y: 2807, Width: 1350, Height: 604},
		},
		{
			ID:       "inner",
			Tag:      "section",
			Heading:  "Mortgage-backed investments",
			Text:     "Mortgage-backed investments. All loans are backed by real estate and secured with a mortgage in favor of investors.",
			Selector: "section.outer section.inner",
			Rect:     browserRect{X: 10, Y: 2817, Width: 1330, Height: 584},
		},
	}

	got := rankRegions(regions, "real estate investment loans investors", 3)
	if len(got) != 1 {
		t.Fatalf("ranked len=%d, want one near-duplicate representative: %#v", len(got), got)
	}
}

func TestRankRegionsPrefersSectionOverStandaloneHeading(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "heading",
			Tag:      "h3",
			Heading:  "All Estateguru loans are backed by real estate and secured with a mortgage in favor of our investors.",
			Text:     "All Estateguru loans are backed by real estate and secured with a mortgage in favor of our investors.",
			Selector: "section.loans h3",
			Rect:     browserRect{X: 20, Y: 3001, Width: 595, Height: 120},
		},
		{
			ID:       "section",
			Tag:      "section",
			Heading:  "Invest in real estate with confidence",
			Text:     "A pioneer in real estate investing. Discover a platform for investing through property-backed loans.",
			Selector: "section.hero",
			Rect:     browserRect{X: 0, Y: 72, Width: 1350, Height: 512},
		},
	}

	got := rankRegions(regions, "real estate platform loans investors", 2)
	if len(got) != 1 {
		t.Fatalf("ranked len=%d, want only section; ranked=%#v", len(got), got)
	}
	if got[0].Region.ID != "section" {
		t.Fatalf("first region=%s, want section; ranked=%#v", got[0].Region.ID, got)
	}
}

func TestRankRegionsPenalizesFooterNavigation(t *testing.T) {
	regions := []browserRegion{
		{
			ID:       "footer",
			Tag:      "footer",
			Text:     "Investing Loans Bonds ETFs Real estate Crypto Smart Cash Fees Company Legal documents",
			Selector: "footer.m-u-padding-top-6",
			Rect:     browserRect{X: 0, Y: 7200, Width: 1350, Height: 780},
		},
		{
			ID:       "hero",
			Tag:      "section",
			Heading:  "Build wealth with confidence",
			Text:     "Investing designed to meet your ambition. Loans Real estate Bonds Smart Cash ETFs.",
			Selector: "#js-page-hero",
			Rect:     browserRect{X: 0, Y: 120, Width: 1350, Height: 620},
		},
	}

	got := rankRegions(regions, "investment products returns loans ETFs bonds", 2)
	if len(got) == 0 {
		t.Fatalf("ranked len=0")
	}
	if got[0].Region.ID != "hero" {
		t.Fatalf("first region=%s, want hero; ranked=%#v", got[0].Region.ID, got)
	}
}

func TestRankRegionsSupportsUnicodeQueries(t *testing.T) {
	regions := []browserRegion{{
		ID: "spanish", Tag: "section", Heading: "Inversión inmobiliaria",
		Text: "Oportunidades de inversión inmobiliaria con préstamos garantizados.",
		Rect: browserRect{X: 0, Y: 100, Width: 1000, Height: 400}, Visible: true,
	}}
	got := rankRegions(regions, "inversión inmobiliaria", 1)
	if len(got) != 1 || got[0].Region.ID != "spanish" {
		t.Fatalf("unicode query did not match: %#v", got)
	}
}

func TestRunPersistenceStoresCompactSummary(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	if _, err := app.toolExtract(ctx, map[string]any{"url": "https://example.com/compact", "store": true, "cache": "bypass"}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	var outputBytes, metadataBytes int
	var summary string
	if err := ctx.AppDB().QueryRow(`SELECT LENGTH(output_json), COALESCE(summary,'') FROM web_runs ORDER BY id DESC LIMIT 1`).Scan(&outputBytes, &summary); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if outputBytes > 2048 || summary == "" {
		t.Fatalf("run payload not compact: bytes=%d summary=%q", outputBytes, summary)
	}
	if err := ctx.AppDB().QueryRow(`SELECT LENGTH(metadata_json) FROM web_artifacts ORDER BY id DESC LIMIT 1`).Scan(&metadataBytes); err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if metadataBytes > 1024 {
		t.Fatalf("artifact metadata duplicated payload: bytes=%d", metadataBytes)
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

func TestExtractSnapshotHonorsStoreFalseAndReusesSession(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)

	outAny, err := app.toolExtract(ctx, map[string]any{
		"url":      "https://example.com/private-evidence",
		"snapshot": true,
		"store":    false,
		"cache":    "bypass",
	})
	if err != nil {
		t.Fatalf("extract snapshot: %v", err)
	}
	page := outAny.(map[string]any)["page"].(pageDoc)
	shot, ok := page.Snapshot.(map[string]any)
	if !ok || stringFromAny(shot["png_b64"]) == "" || boolFromMap(shot, "stored") {
		t.Fatalf("snapshot should be returned inline and not stored: %#v", page.Snapshot)
	}
	if got := countCalls(plat, "storage", "files_upload"); got != 0 {
		t.Fatalf("store=false uploaded %d files", got)
	}
	if got := countCalls(plat, "computer", "browser_open"); got != 1 {
		t.Fatalf("extract+snapshot opened %d sessions, want 1", got)
	}
}

func TestSnapshotArtifactInsertFailureReturnsErrorAndRollsBackUpload(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	if err := ctx.AppDB().Close(); err != nil {
		t.Fatalf("close app db: %v", err)
	}
	out := map[string]any{"current_url": "https://example.com", "heading": "Example"}
	if err := app.storeSnapshotImage(ctx, 0, out, testPNGB64(), "snapshot", "Example", 0); err == nil {
		t.Fatal("expected artifact insert error")
	}
	if got := countCalls(plat, "storage", "files_delete"); got != 1 {
		t.Fatalf("rollback delete calls=%d, want 1", got)
	}
}

func TestCacheKeyPreservesCrawlSeedOrder(t *testing.T) {
	_, first, err := cacheKey("crawl", map[string]any{"urls": []string{"https://a.example", "https://b.example"}, "max_pages": 1})
	if err != nil {
		t.Fatalf("first cache key: %v", err)
	}
	_, second, err := cacheKey("crawl", map[string]any{"urls": []string{"https://b.example", "https://a.example"}, "max_pages": 1})
	if err != nil {
		t.Fatalf("second cache key: %v", err)
	}
	if first == second {
		t.Fatal("ordered crawl seeds produced the same cache key")
	}
}

func TestSearchRedirectDecodingPreservesEscapedPath(t *testing.T) {
	want := "https://example.com/a%2Fb"
	google := decodeGoogleURL("https://www.google.com/url?q=https%3A%2F%2Fexample.com%2Fa%252Fb")
	if google != want {
		t.Fatalf("google redirect=%q, want %q", google, want)
	}
	duck := decodeDuckURL("https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%252Fb")
	if duck != want {
		t.Fatalf("duck redirect=%q, want %q", duck, want)
	}
	if isGoogleOwnedHost("notgoogle.com") {
		t.Fatal("lookalike domain classified as Google-owned")
	}
}

func TestResearchBoundsQueriesAndDoesNotCreateNestedRuns(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	queries := make([]string, 12)
	for i := range queries {
		queries[i] = fmt.Sprintf("bounded research query %d", i)
	}
	outAny, err := app.toolResearch(ctx, map[string]any{
		"question":    "What is bounded research?",
		"queries":     queries,
		"max_results": 1,
		"max_sources": 1,
		"store":       false,
		"cache":       "bypass",
	})
	if err != nil {
		t.Fatalf("research: %v", err)
	}
	out := outAny.(map[string]any)
	if got := len(out["queries"].([]string)); got != maxResearchQueries {
		t.Fatalf("queries=%d, want cap %d", got, maxResearchQueries)
	}
	var total, research, search int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*), SUM(kind='research'), SUM(kind='search') FROM web_runs`).Scan(&total, &research, &search); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if total != 1 || research != 1 || search != 0 {
		t.Fatalf("run counts total=%d research=%d search=%d", total, research, search)
	}
}

func TestPrivateNetworkTargetsAreBlockedByDefault(t *testing.T) {
	plat := newFakePlatform()
	ctx, _ := newTestCtx(t, plat, tk.WithConfig(map[string]string{"allow_private_networks": "false"}))
	for _, target := range []string{
		"http://127.0.0.1/admin",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/",
		"http://service.localhost/",
	} {
		if err := validateBrowserTarget(ctx, target); err == nil {
			t.Errorf("target %s was not blocked", target)
		}
	}
}

func TestSnapshotCacheRejectsInlineImagePayloads(t *testing.T) {
	policy, err := newCachePolicy("snapshot", map[string]any{"store": false, "max_age": 60})
	if err != nil {
		t.Fatalf("cache policy: %v", err)
	}
	if policy.Read || policy.Write {
		t.Fatalf("inline snapshot cache enabled: %#v", policy)
	}
	cached, err := cloneResponseForCache(map[string]any{"png_b64": "secret-image", "shots": []any{map[string]any{"png_b64": "nested-image"}}})
	if err != nil {
		t.Fatalf("clone cache response: %v", err)
	}
	if _, ok := cached["png_b64"]; ok {
		t.Fatal("top-level image remained in cache payload")
	}
	shots := cached["shots"].([]any)
	if _, ok := shots[0].(map[string]any)["png_b64"]; ok {
		t.Fatal("nested image remained in cache payload")
	}
}

func TestInterruptedRunsRecoveredAndOldHistoryPruned(t *testing.T) {
	plat := newFakePlatform()
	ctx, _ := newTestCtx(t, plat, tk.WithConfig(map[string]string{
		"allow_private_networks": "true",
		"history_retention_days": "1",
	}))
	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := ctx.AppDB().Exec(`INSERT INTO web_runs(project_id,kind,input_json,status,created_at) VALUES('global','extract','{}','running',?)`, old); err != nil {
		t.Fatalf("insert old run: %v", err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO web_runs(project_id,kind,input_json,status) VALUES('global','extract','{}','running')`); err != nil {
		t.Fatalf("insert current run: %v", err)
	}
	if err := recoverInterruptedRuns(ctx); err != nil {
		t.Fatalf("recover runs: %v", err)
	}
	var running int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM web_runs WHERE status='running'`).Scan(&running); err != nil {
		t.Fatalf("count running: %v", err)
	}
	if running != 0 {
		t.Fatalf("running rows=%d, want 0", running)
	}
	if err := pruneHistory(ctx); err != nil {
		t.Fatalf("prune history: %v", err)
	}
	var remaining int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM web_runs`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining rows=%d, want 1", remaining)
	}
}

type fakeCall struct {
	app, tool string
	args      map[string]any
}

type fakePlatform struct {
	tk.BasePlatformClient
	mu                  sync.Mutex
	calls               []fakeCall
	storageID           int64
	storageURL          string
	openURL             string
	searchBlocked       bool
	cookieBanner        bool
	cookieBannerSOM     bool
	cookieTextBanner    bool
	cookiePolicyText    bool
	cookieDismissed     bool
	duplicateCrawlLinks bool
	extractorPagination bool
	extractorPage       int
	scrollY             int
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{storageID: 10, storageURL: "https://storage.test/signed/10"}
}

func (p *fakePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, fakeCall{app: app, tool: tool, args: copyArgs(in)})
	resp := p.respond(app, tool, in)
	p.mu.Unlock()
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
		if p.extractorPagination {
			p.extractorPage = 1
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
		if p.extractorPagination {
			regions := []map[string]any{}
			if p.extractorPage < 2 {
				regions = append(regions, map[string]any{"id": "next", "tag": "a", "role": "link", "text": "Next", "selector": ".next", "visible": true, "viewport_rect": map[string]any{"x": 100, "y": 200, "width": 80, "height": 30}, "rect": map[string]any{"x": 100, "y": 200, "width": 80, "height": 30}})
			}
			return map[string]any{"session_id": in["session_id"], "backend": "local", "current_url": p.openURL, "url": p.openURL, "title": "Products", "html": fmt.Sprintf(`<html><body><article><h1>Page %d</h1></article><a class="next">Next</a></body></html>`, p.extractorPage), "regions": regions, "rendered": true, "extraction_backend": "browser_dom", "width": 1280, "height": 720}
		}
		links := []map[string]any{{"url": p.openURL + "/next", "text": "Next page"}}
		if p.duplicateCrawlLinks {
			if strings.HasSuffix(p.openURL, "/start") {
				links = []map[string]any{
					{"url": "https://example.com/a", "text": "A"},
					{"url": "https://example.com/a", "text": "A duplicate"},
					{"url": "https://example.com/b", "text": "B"},
				}
			} else {
				links = []map[string]any{}
			}
		}
		regions := []map[string]any{{
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
				"y":      1100 - p.scrollY,
				"width":  520,
				"height": 180,
			},
			"coordinate_frame": "document_css_px",
			"visible":          false,
		}}
		if p.cookieBanner && !p.cookieDismissed {
			regions = append([]map[string]any{{
				"id":       "r_cookie",
				"tag":      "div",
				"heading":  "Cookies on Mintos",
				"text":     "Cookies on Mintos. We use cookies to improve your experience. Select cookies. Accept necessary. Accept all.",
				"selector": "#onetrust-banner-sdk",
				"rect": map[string]any{
					"x":      357,
					"y":      90,
					"width":  650,
					"height": 232.765625,
				},
				"viewport_rect": map[string]any{
					"x":      357,
					"y":      90,
					"width":  650,
					"height": 232.765625,
				},
				"coordinate_frame": "document_css_px",
				"visible":          true,
			}}, regions...)
		}
		text := "Hello This page has useful text."
		html := "<html><body><h1>Hello</h1><p>This page has useful text.</p></body></html>"
		if p.cookiePolicyText {
			text += " Privacy notice and cookies policy."
			html += `<footer><a href="/cookies">Cookies policy</a></footer>`
		}
		if p.cookieTextBanner && !p.cookieDismissed {
			text += " We use cookies and similar technologies to help personalize content and provide a better experience. I accept."
			html += `<div class="cookie-bar"><p>We use cookies and similar technologies to help personalize content, tailor and measure ads, and provide a better experience.</p><button>I accept</button></div>`
		}
		return map[string]any{
			"session_id":         in["session_id"],
			"backend":            "local",
			"current_url":        p.openURL,
			"url":                p.openURL,
			"title":              "Readable Page",
			"description":        "A page for extraction",
			"text":               text,
			"markdown":           "# Hello\n\nThis page has useful text.",
			"html":               html,
			"links":              links,
			"regions":            regions,
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
		if in["action"] == "scroll" {
			amount := intFromAny(in["amount"])
			if stringFromAny(in["direction"]) == "up" {
				amount = -amount
			}
			p.scrollY = maxInt(0, p.scrollY+amount)
		}
		if in["action"] == "click" && stringFromAny(in["coordinate"]) != "" && p.cookieBanner {
			p.cookieDismissed = true
		}
		if in["action"] == "click" && stringFromAny(in["coordinate"]) != "" && p.extractorPagination && p.extractorPage < 2 {
			p.extractorPage++
		}
		if in["action"] == "click" && in["label"] != nil && p.cookieTextBanner {
			p.cookieDismissed = true
		}
		if in["action"] == "click" && intFromAny(in["label"]) == 7 && p.cookieBanner {
			p.cookieDismissed = true
		}
		out := map[string]any{"current_url": p.openURL, "width": 1280, "height": 720}
		if in["action"] == "screenshot" && in["include_som"] == true && !p.cookieDismissed {
			targets := []map[string]any{}
			if p.cookieBannerSOM && p.cookieBanner {
				targets = append(targets, map[string]any{
					"label": 7, "x": 830, "y": 235, "w": 140, "h": 42,
					"tag": "button", "role": "button", "text": "Accept all",
				})
			}
			if p.cookieTextBanner {
				targets = append(targets, map[string]any{
					"label": 9, "x": 1068, "y": 740, "w": 128, "h": 44,
					"tag": "button", "role": "button", "text": "I accept",
				})
			}
			if len(targets) > 0 {
				out["som"] = targets
			}
		}
		return out
	case "computer.browser_screenshot":
		return map[string]any{
			"png_b64":     testPNGB64(),
			"current_url": "https://example.com",
			"width":       1280,
			"height":      720,
		}
	case "storage.files_upload":
		return map[string]any{"id": p.storageID, "url": p.storageURL}
	case "jobs.jobs_schedule":
		return map[string]any{"job": map[string]any{"id": 77, "name": in["name"], "owner_app": "web", "status": "pending", "target": in["target"]}}
	case "jobs.jobs_list":
		return map[string]any{"jobs": []map[string]any{{"id": 77, "name": "Products", "owner_app": "web", "status": "pending", "target": map[string]any{"app": "web", "tool": "web_extractor_run"}}}, "count": 1}
	case "jobs.jobs_runs":
		return map[string]any{"runs": []any{}}
	case "jobs.jobs_cancel":
		return map[string]any{"cancelled": true, "id": in["id"]}
	case "jobs.jobs_run_now":
		return map[string]any{"queued": true, "id": in["id"]}
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

func (p *fakePlatform) callsSnapshot() []fakeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]fakeCall, len(p.calls))
	copy(out, p.calls)
	return out
}

func countCalls(p *fakePlatform, app, tool string) int {
	count := 0
	for _, call := range p.callsSnapshot() {
		if call.app == app && call.tool == tool {
			count++
		}
	}
	return count
}

func newTestCtx(t *testing.T, plat *fakePlatform, extra ...tk.Option) (*sdk.AppCtx, *App) {
	t.Helper()
	opts := append([]tk.Option{
		tk.WithPlatform(plat),
		tk.WithConfig(map[string]string{"allow_private_networks": "true"}),
	}, extra...)
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
