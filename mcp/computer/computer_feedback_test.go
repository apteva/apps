package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/elementread"
)

func TestNavigationURLsSameDocumentIgnoresFragment(t *testing.T) {
	cases := []struct {
		left, right string
		want        bool
	}{
		{"https://app.test/p?screen=x", "https://app.test/p?screen=x#/x", true},
		{"https://app.test/p?screen=x#/a", "https://app.test/p?screen=x#/b", true},
		{"https://APP.test", "https://app.test/#top", true},
		{"https://app.test/p?screen=x", "https://app.test/p?screen=y#/x", false},
		{"https://app.test/p", "https://app.test/q#/p", false},
	}
	for _, tc := range cases {
		if got := navigationURLsSameDocument(tc.left, tc.right); got != tc.want {
			t.Errorf("navigationURLsSameDocument(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
	// History moves between fragments remain real moves for back.
	if !navigationURLChanged("https://app.test/p#/a", "https://app.test/p#/b") {
		t.Fatal("back between hash routes must still count as a URL change")
	}
}

func TestNavigateAcceptsHashRewrite(t *testing.T) {
	const requested = "https://app.test/page?screen=x"
	const landed = requested + "#/x"
	for _, tc := range []struct {
		name, start string
		reload      bool
	}{
		{name: "from another page", start: "https://app.test/other"},
		{name: "already on the rewritten URL", start: landed, reload: true},
		{name: "already on the requested URL", start: requested, reload: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, png: []byte{0x89, 0x50, 0x4e, 0x47}, url: tc.start}
			fake.executeHook = func(action backends.Action) error {
				if action.Type != "navigate" {
					return fmt.Errorf("unexpected action %q", action.Type)
				}
				fake.mu.Lock()
				fake.url = action.URL + "#/x" // the app rewrites with a hash route
				fake.mu.Unlock()
				return nil
			}
			app := appWithSession("br_hash", fake, "browserbase")
			ctx := tk.NewAppCtx(t, "apteva.yaml")
			out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_hash", "action": "navigate", "url": requested})
			if err != nil {
				t.Fatalf("navigate: %v", err)
			}
			m := out.(map[string]any)
			if m["current_url"] != landed {
				t.Fatalf("current_url = %v", m["current_url"])
			}
			if tc.reload && m["reloaded"] != true {
				t.Fatalf("same-document navigate should report a reload: %#v", m)
			}
		})
	}

	t.Run("staying elsewhere still fails", func(t *testing.T) {
		fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, png: []byte{0x89, 0x50, 0x4e, 0x47}, url: "https://app.test/other#/x"}
		fake.executeHook = func(backends.Action) error { return nil }
		app := appWithSession("br_stuck", fake, "browserbase")
		ctx := tk.NewAppCtx(t, "apteva.yaml")
		if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_stuck", "action": "navigate", "url": requested}); err == nil || !strings.Contains(err.Error(), "navigation_ineffective") {
			t.Fatalf("navigate that never left another page must fail: %v", err)
		}
	})
}

type readingComp struct {
	*fakeComp
	read         backends.ElementReadResult
	readErr      error
	gotSelector  string
	gotOpts      backends.ElementReadOptions
	diag         backends.PageDiagnostics
	diagCalls    []string
	enabled      int
	masks        []backends.ScreenshotMask
	maskErr      error
	restored     int
	maskedDuring bool
}

func (r *readingComp) ReadElement(selector string, opts backends.ElementReadOptions) (backends.ElementReadResult, error) {
	r.gotSelector, r.gotOpts = selector, opts
	return r.read, r.readErr
}

func (r *readingComp) EnablePageDiagnostics() { r.enabled++ }

func (r *readingComp) PageDiagnostics(since uint64, clear bool) backends.PageDiagnostics {
	r.diagCalls = append(r.diagCalls, fmt.Sprintf("%d/%v", since, clear))
	out := r.diag
	out.Enabled = true
	return out
}

func (r *readingComp) MaskForScreenshot(mask backends.ScreenshotMask) (func(), error) {
	if r.maskErr != nil {
		return nil, r.maskErr
	}
	r.masks = append(r.masks, mask)
	r.maskedDuring = true
	return func() { r.restored++; r.maskedDuring = false }, nil
}

func newReadingComp() *readingComp {
	return &readingComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, png: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, url: "https://app.test/"}}
}

func TestComputerUseReadElement(t *testing.T) {
	comp := newReadingComp()
	comp.read = backends.ElementReadResult{Selector: "#fx-api-test-status", Count: 1, Elements: []backends.ElementRead{
		{Tag: "div", Text: "ready", Visible: false, Attributes: map[string]string{"data-status": "ready", "hidden": ""}},
	}}
	app := appWithSession("br_read", comp, "local")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_read", "action": "read_element", "selector": " #fx-api-test-status "})
	if err != nil {
		t.Fatalf("read_element: %v", err)
	}
	m := out.(map[string]any)
	if m["text"] != "ready" || m["count"] != 1 || m["visible"] != false || m["found"] != true || comp.gotSelector != "#fx-api-test-status" || comp.gotOpts.All || comp.gotOpts.VisibleOnly {
		t.Fatalf("unexpected read_element result: %#v (opts %#v)", m, comp.gotOpts)
	}
	if attrs, _ := m["attributes"].(map[string]string); attrs["data-status"] != "ready" {
		t.Fatalf("attributes missing: %#v", m)
	}
	if _, present := m["value"]; present {
		t.Fatalf("div has no value: %#v", m)
	}
	if comp.lastAction.Type != "" {
		t.Fatalf("read_element must not dispatch an action, got %q", comp.lastAction.Type)
	}

	comp.read = backends.ElementReadResult{Selector: "#password", Count: 1, Elements: []backends.ElementRead{{Tag: "input", Visible: true, ValueRedacted: true, Redacted: true}}}
	out, err = app.toolComputerUse(ctx, map[string]any{"session_id": "br_read", "action": "read_element", "selector": "#password", "all": true, "include_hidden": false})
	if err != nil {
		t.Fatalf("read_element password: %v", err)
	}
	m = out.(map[string]any)
	if _, present := m["value"]; present || m["value_redacted"] != true || !comp.gotOpts.All || !comp.gotOpts.VisibleOnly || m["elements"] == nil {
		t.Fatalf("password must not expose a value: %#v", m)
	}

	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_read", "action": "read_element"}); err == nil || !strings.Contains(err.Error(), "invalid_target") {
		t.Fatalf("missing selector should be invalid_target: %v", err)
	}
	comp.readErr = fmt.Errorf("%w: bad", elementread.ErrInvalidSelector)
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_read", "action": "read_element", "selector": "div["}); err == nil || !strings.Contains(err.Error(), "invalid_target") {
		t.Fatalf("invalid selector should be invalid_target: %v", err)
	}

	plain := appWithSession("svc", &fakeComp{display: backends.DisplaySize{Width: 1, Height: 1}}, "service")
	if _, err := plain.toolComputerUse(ctx, map[string]any{"session_id": "svc", "action": "read_element", "selector": "body"}); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("backend without reader should be unsupported: %v", err)
	}
}

func TestSelectorTextWaitCondition(t *testing.T) {
	got, err := waitConditionsArg([]any{map[string]any{"type": "selector_text", "selector": "#s", "value": "ready"}})
	if err != nil || len(got) != 1 || got[0].Op != "equals" {
		t.Fatalf("selector_text defaults to equals: %#v %v", got, err)
	}
	if _, err := waitConditionsArg([]any{map[string]any{"type": "selector_text", "selector": "#s", "value": "", "op": "equals"}}); err != nil {
		t.Fatalf("equals may wait for an empty text: %v", err)
	}
	for _, bad := range []map[string]any{
		{"type": "selector_text", "value": "x"},
		{"type": "selector_text", "selector": "#s"},
		{"type": "selector_text", "selector": "#s", "value": "", "op": "contains"},
		{"type": "selector_text", "selector": "#s", "value": "x", "op": "regex"},
	} {
		if _, err := waitConditionsArg([]any{bad}); err == nil {
			t.Fatalf("expected rejection for %#v", bad)
		}
	}
}

func TestBrowserDiagnosticsOptIn(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	comp := newReadingComp()
	comp.diag = backends.PageDiagnostics{Exceptions: []string{"TypeError: x is undefined"}, ConsoleErrors: 2, Cursor: 7,
		Requests: []backends.PageRequestDiagnostic{{Method: "GET", Origin: "https://app.test", Path: "/api/calls", Status: 500}}}
	newBackend = func(backends.Config) (backends.Computer, error) { return comp, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	plainOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	plainID := plainOut.(map[string]any)["session_id"].(string)
	if comp.enabled != 0 {
		t.Fatal("diagnostics must not record unless requested")
	}
	off, err := app.toolBrowserDiagnostics(ctx, map[string]any{"session_id": plainID})
	if err != nil || off.(map[string]any)["enabled"] != false || len(comp.diagCalls) != 0 {
		t.Fatalf("disabled session should report enabled=false without reading: %#v %v", off, err)
	}

	out, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local", "diagnostics": true})
	if err != nil {
		t.Fatalf("open with diagnostics: %v", err)
	}
	id := out.(map[string]any)["session_id"].(string)
	if comp.enabled != 1 {
		t.Fatalf("diagnostics=true must enable recording before the page opens, got %d", comp.enabled)
	}
	status, err := app.toolBrowserSession(ctx, map[string]any{"action": "status", "session_id": id})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	summary, ok := status.(map[string]any)["page_diagnostics"].(map[string]any)
	if !ok || summary["exceptions"] != 1 || summary["console_errors"] != 2 || summary["requests"] != 1 {
		t.Fatalf("status summary: %#v", status)
	}
	read, err := app.toolBrowserDiagnostics(ctx, map[string]any{"session_id": id, "since": 3, "clear": true})
	if err != nil {
		t.Fatalf("browser_diagnostics: %v", err)
	}
	m := read.(map[string]any)
	if m["enabled"] != true || m["console_errors"] != 2 || m["cursor"] != uint64(7) {
		t.Fatalf("diagnostics read: %#v", m)
	}
	if want := "0/false 3/true"; strings.Join(comp.diagCalls, " ") != want {
		t.Fatalf("status must peek and the tool must pass since/clear: %v", comp.diagCalls)
	}

	newBackend = func(backends.Config) (backends.Computer, error) {
		return &fakeComp{display: backends.DisplaySize{Width: 1, Height: 1}, url: "https://app.test/"}, nil
	}
	if _, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local", "diagnostics": true}); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("unsupported backend must fail closed: %v", err)
	}
}

func TestBrowserScreenshotMask(t *testing.T) {
	comp := newReadingComp()
	app := appWithSession("br_mask", comp, "local")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": "br_mask", "mask": map[string]any{"inputs": true, "secrets": []any{"hunter2"}}})
	if err != nil {
		t.Fatalf("masked screenshot: %v", err)
	}
	m := out.(map[string]any)
	if len(comp.masks) != 1 || !comp.masks[0].Inputs || comp.masks[0].Secrets[0] != "hunter2" || comp.restored != 1 || comp.maskedDuring {
		t.Fatalf("mask must be applied once and restored: %#v restored=%d", comp.masks, comp.restored)
	}
	if _, ok := m["screenshot_url"]; ok || m["masked"] != true {
		t.Fatalf("masked result must not link the live unmasked frame: %#v", m)
	}
	if fmt.Sprint(m) != "" && strings.Contains(fmt.Sprint(m), "hunter2") {
		t.Fatal("secret leaked into the response")
	}

	plain, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": "br_mask"})
	if err != nil || plain.(map[string]any)["screenshot_url"] == nil || len(comp.masks) != 1 {
		t.Fatalf("unmasked screenshot unchanged: %#v %v", plain, err)
	}

	comp.maskErr = fmt.Errorf("evaluate failed with hunter2")
	if _, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": "br_mask", "mask": map[string]any{"secrets": []any{"hunter2"}}}); err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "mask_failed") {
		t.Fatalf("failed mask must refuse without echoing secrets: %v", err)
	}
	if _, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": "br_mask", "mask": map[string]any{"secrets": []any{""}}}); err == nil || !strings.Contains(err.Error(), "invalid_mask") {
		t.Fatalf("empty secret must be rejected: %v", err)
	}
	other := appWithSession("svc", &fakeComp{display: backends.DisplaySize{Width: 1, Height: 1}, png: []byte{0x89, 0x50, 0x4e, 0x47}}, "service")
	if _, err := other.toolBrowserScreenshot(ctx, map[string]any{"session_id": "svc", "mask": map[string]any{"inputs": true}}); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("backend without masking must not capture unmasked: %v", err)
	}
}

func TestContextDeletePurgesLocalProfile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APTEVA_LOCAL_CONTEXT_DIR", base)
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	created, err := app.toolContextCreate(ctx, map[string]any{"name": "login", "backend": "local"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	row := created.(map[string]any)["context"].(*ComputerContext)
	profile := filepath.Join(base, row.ProviderContextID)
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Default", "Cookies"), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}

	app.reg.put("br_busy", &session{comp: &fakeComp{}, backend: "local", appContextID: row.ID, openedAt: time.Now()})
	if _, err := app.toolContextDelete(ctx, map[string]any{"id": row.ID, "purge_profile": true}); err == nil || !strings.Contains(err.Error(), "br_busy") {
		t.Fatalf("purge must be refused while a session uses the context: %v", err)
	}
	app.reg.remove("br_busy")

	out, err := app.toolContextDelete(ctx, map[string]any{"id": row.ID, "purge_profile": true})
	if err != nil {
		t.Fatalf("delete with purge: %v", err)
	}
	if out.(map[string]any)["profile_purged"] != true {
		t.Fatalf("purge result: %#v", out)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile folder should be gone: %v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("contexts base dir must survive: %v", err)
	}

	bb, err := app.toolContextCreate(ctx, map[string]any{"name": "cloud", "backend": "browserbase", "provider_context_id": "ctx_remote"})
	if err != nil {
		t.Fatalf("create browserbase: %v", err)
	}
	if _, err := app.toolContextDelete(ctx, map[string]any{"id": bb.(map[string]any)["context"].(*ComputerContext).ID, "purge_profile": true}); err == nil || !strings.Contains(err.Error(), "only to local") {
		t.Fatalf("purge on a provider context must be refused: %v", err)
	}
}

func TestComputerUseSchemaListsNewSurface(t *testing.T) {
	tools := (&App{}).MCPTools()
	tool := findTool(t, tools, "computer_use")
	properties := tool.InputSchema["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	found := false
	for _, a := range actions {
		found = found || a == "read_element"
	}
	if !found {
		t.Fatal("computer_use action enum missing read_element")
	}
	for _, key := range []string{"all", "include_hidden"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("computer_use schema missing %q", key)
		}
	}
	findTool(t, tools, "browser_diagnostics")
	shot := findTool(t, tools, "browser_screenshot").InputSchema["properties"].(map[string]any)
	if _, ok := shot["mask"]; !ok {
		t.Fatal("browser_screenshot schema missing mask")
	}
}
