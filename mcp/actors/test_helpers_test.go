package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeCall struct {
	app, tool string
	args      map[string]any
}

type fakePlatform struct {
	tk.BasePlatformClient
	failAction            string
	blockExtract          chan struct{}
	mu                    sync.Mutex
	calls                 []fakeCall
	storageID             int64
	storageURL            string
	openURL               string
	searchBlocked         bool
	searchTruncatedFirst  bool
	searchTruncatedAlways bool
	searchExtractCount    int
	cookieBanner          bool
	cookieBannerSOM       bool
	cookieTextBanner      bool
	cookiePolicyText      bool
	cookieDismissed       bool
	duplicateCrawlLinks   bool
	actorPagination       bool
	actorPage             int
	scrollY               int
	selectorRedirectURL   string
	openBackendOverride   string
	proxyModeOverride     string
	proxyCountryOverride  string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{storageID: 10, storageURL: "https://storage.test/signed/10"}
}

func (p *fakePlatform) CallAppResultContext(ctx context.Context, app, tool string, in map[string]any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tool == "browser_extract" && p.blockExtract != nil {
		close(p.blockExtract)
		<-ctx.Done()
		return ctx.Err()
	}
	return p.CallAppResult(app, tool, in, out)
}

func (p *fakePlatform) CallAppContext(ctx context.Context, app, tool string, in map[string]any) (json.RawMessage, error) {
	var out map[string]any
	if err := p.CallAppResultContext(ctx, app, tool, in, &out); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (p *fakePlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, fmt.Errorf("batch calls are not used by Actors")
}

var _ sdk.AppContextClient = (*fakePlatform)(nil)

func (p *fakePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, fakeCall{app: app, tool: tool, args: copyArgs(in)})
	if tool == "computer_use" && p.failAction != "" && in["action"] == p.failAction {
		p.mu.Unlock()
		return fmt.Errorf("simulated uncertain action failure")
	}
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
		if p.actorPagination {
			p.actorPage = 1
		}
		backend := firstNonEmpty(p.openBackendOverride, stringFromAny(in["backend"]), "local")
		proxyMode := firstNonEmpty(p.proxyModeOverride, stringFromAny(in["proxy_mode"]), "auto")
		if proxyMode == "none" {
			proxyMode = "direct"
		}
		proxyCountry := firstNonEmpty(p.proxyCountryOverride, stringFromAny(in["proxy_country"]))
		proxy := map[string]any{"mode": proxyMode, "country": proxyCountry}
		if profile := stringFromAny(in["proxy_profile"]); profile != "" {
			proxy["profile_id"] = profile
			proxy["profile_name"] = profile
		}
		if sticky := stringFromAny(in["proxy_sticky"]); sticky != "" {
			proxy["sticky_scope"] = sticky
		}
		return map[string]any{
			"session_id":  "sess_1",
			"backend":     backend,
			"current_url": in["url"],
			"width":       1280,
			"height":      720,
			"proxy":       proxy,
		}
	case "computer.browser_extract":
		if strings.Contains(p.openURL, "google.com/search") {
			p.searchExtractCount++
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
			if p.searchTruncatedAlways || (p.searchTruncatedFirst && p.searchExtractCount == 1) {
				return map[string]any{
					"session_id":         in["session_id"],
					"backend":            "local",
					"current_url":        p.openURL,
					"title":              "Google Search",
					"text":               "Peer-To-Peer Lending Affiliate Programs Affiliate Partnerships",
					"truncated":          true,
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
		if p.actorPagination {
			regions := []map[string]any{}
			if p.actorPage < 2 {
				regions = append(regions, map[string]any{"id": "next", "tag": "a", "role": "link", "text": "Next", "selector": ".next", "visible": true, "viewport_rect": map[string]any{"x": 100, "y": 200, "width": 80, "height": 30}, "rect": map[string]any{"x": 100, "y": 200, "width": 80, "height": 30}})
			}
			return map[string]any{"session_id": in["session_id"], "backend": "local", "current_url": p.openURL, "url": p.openURL, "title": "Products", "html": fmt.Sprintf(`<html><body><article><h1>Page %d</h1></article><a class="next">Next</a></body></html>`, p.actorPage), "regions": regions, "rendered": true, "extraction_backend": "browser_dom", "width": 1280, "height": 720}
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
			p.scrollY = max(0, p.scrollY+amount)
		}
		if in["action"] == "click" && stringFromAny(in["coordinate"]) != "" && p.cookieBanner {
			p.cookieDismissed = true
		}
		if in["action"] == "click" && stringFromAny(in["coordinate"]) != "" && p.actorPagination && p.actorPage < 2 {
			p.actorPage++
		}
		if in["action"] == "click" && in["label"] != nil && p.cookieTextBanner {
			p.cookieDismissed = true
		}
		if in["action"] == "click" && intFromAny(in["label"]) == 7 && p.cookieBanner {
			p.cookieDismissed = true
		}
		if in["action"] == "click" && stringFromAny(in["selector"]) != "" && p.selectorRedirectURL != "" {
			p.openURL = p.selectorRedirectURL
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
		return map[string]any{"job": map[string]any{"id": 77, "name": in["name"], "owner_app": "actors", "status": "pending", "target": in["target"]}}
	case "jobs.jobs_list":
		return map[string]any{"jobs": []map[string]any{{"id": 77, "name": "Products", "owner_app": "actors", "status": "pending", "target": map[string]any{"app": "actors", "tool": "actors_run"}}}, "count": 1}
	case "jobs.jobs_runs":
		return map[string]any{"runs": []any{}}
	case "jobs.jobs_get":
		owner := "actors"
		if intFromAny(in["id"]) == 999 {
			owner = "other"
		}
		return map[string]any{"job": map[string]any{"id": in["id"], "owner_app": owner, "target": map[string]any{"app": owner, "tool": "actors_run"}}, "found": true}
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
