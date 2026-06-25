package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestComputerAppBrowserSoMLabelClick is the app-side Tier 2 test: it
// launches the real sidecar, opens a real browser backend, verifies the
// screenshot has badge-like colored pixels, and proves label=1 clicks
// through from the latest screenshot. It is opt-in because it launches
// Chrome or a paid cloud browser.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserSoMLabelClick -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserSoMLabelClick -timeout 5m .
func TestComputerAppBrowserSoMLabelClick(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser SoM test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     "https://example.com",
		"viewport": map[string]any{
			"width":  1600,
			"height": 800,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	shot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	raw := decodeScreenshot(t, shot)
	if !hasBadgeLikePixels(t, raw) {
		t.Fatal("screenshot did not contain expected Set-of-Mark badge-colored pixels")
	}

	_ = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      1,
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out := sc.MCP("computer_use", map[string]any{
			"session_id": sessionID,
			"action":     "wait",
			"duration":   500,
		})
		if strings.Contains(strings.ToLower(stringValue(out["current_url"])), "iana.org") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("label=1 click did not navigate Example Domain's Learn More link to iana.org")
}

// TestComputerAppBrowserDateTimeTyping is an opt-in browser regression test
// for native temporal inputs. It uses a deterministic local HTTP fixture
// instead of a public site, then verifies real computer_use(type) calls update
// native date/time/datetime-local values.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserDateTimeTyping -timeout 3m .
func TestComputerAppBrowserDateTimeTyping(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser date/time test")
	}
	if backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND")); backend != "" && backend != "local" {
		t.Skip("date/time typing test uses a local httptest page and currently runs against the local backend")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(temporalInputsHTML()))
	}))
	t.Cleanup(srv.Close)

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     srv.URL,
		"viewport": map[string]any{
			"width":  1000,
			"height": 700,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	type field struct {
		name string
		x, y int
		text string
		want string
	}
	for _, f := range []field{
		{name: "date", x: 260, y: 104, text: "2026-06-05", want: "2026-06-05"},
		{name: "time", x: 260, y: 184, text: "08:00 PM", want: "20:00"},
		{name: "datetime", x: 260, y: 264, text: "2026-06-05 08:00 PM", want: "2026-06-05T20:00"},
	} {
		_ = sc.MCP("computer_use", map[string]any{
			"session_id": sessionID,
			"action":     "click",
			"coordinate": fcoord(f.x, f.y),
		})
		out := sc.MCP("computer_use", map[string]any{
			"session_id": sessionID,
			"action":     "type",
			"text":       f.text,
		})
		values := hashValues(stringValue(out["current_url"]))
		if got := values[f.name]; got != f.want {
			t.Fatalf("%s input: want hash value %q, got %q (url=%s values=%v)", f.name, f.want, got, stringValue(out["current_url"]), values)
		}
	}
}

// TestComputerAppBrowserShortcutKeys verifies that browser/editor command
// keys are dispatched as real key events instead of literal text. It defaults
// to local and can also run against Browserbase because the fixture is a data:
// URL and does not need localhost access.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserShortcutKeys -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserShortcutKeys -timeout 5m .
func TestComputerAppBrowserShortcutKeys(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser shortcut key test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     shortcutKeysDataURL(),
		"viewport": map[string]any{
			"width":  900,
			"height": 500,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "click", "coordinate": fcoord(260, 104)})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "key", "key": "Tab"})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "click", "coordinate": fcoord(260, 104)})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "key", "key": "Control+A"})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "type", "text": "X"})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "key", "key": "Backspace"})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "type", "text": "Z"})
	_ = sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "key", "key": "Control+Z"})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out := sc.MCP("computer_use", map[string]any{
			"session_id": sessionID,
			"action":     "wait",
			"duration":   250,
		})
		if strings.Contains(stringValue(out["current_url"]), "computer_key_test=pass") {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("shortcut key test did not reach pass URL")
}

// TestComputerAppBrowserNewTabAutoFollow verifies that a normal target=_blank
// click opens a real tab and Computer follows it when exactly one new tab is
// created. It defaults to local and can also run against Browserbase.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserNewTabAutoFollow -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserNewTabAutoFollow -timeout 5m .
func TestComputerAppBrowserNewTabAutoFollow(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser new-tab test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     newTabFixtureDataURL(),
		"viewport": map[string]any{
			"width":  900,
			"height": 500,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": fcoord(110, 92),
	})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stringValue(out["current_url"]), "computer-tab-test") {
			if out["switched_tab"] != true {
				t.Fatalf("expected switched_tab=true after target blank click, got %v", out)
			}
			if n, ok := numericValue(out["tab_count"]); !ok || n < 2 {
				t.Fatalf("expected at least 2 tabs after target blank click, got %v", out["tab_count"])
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
		out = sc.MCP("computer_use", map[string]any{
			"session_id": sessionID,
			"action":     "wait",
			"duration":   500,
		})
	}
	t.Fatalf("new-tab click did not auto-follow to target page; last out=%v", out)
}

// TestComputerAppBrowserSwitchTab verifies the public MCP tab workflow agents
// should use: list tabs, switch by tab_id, then take a fresh screenshot.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserSwitchTab -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserSwitchTab -timeout 5m .
func TestComputerAppBrowserSwitchTab(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser switch-tab test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     newTabFixtureDataURL(),
		"viewport": map[string]any{
			"width":  900,
			"height": 500,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	clickOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": fcoord(110, 92),
	})
	newTabs, _ := clickOut["new_tabs"].([]any)
	if len(newTabs) != 1 {
		t.Fatalf("expected one new tab, got %v", clickOut["new_tabs"])
	}
	newTab, _ := newTabs[0].(map[string]any)
	newTabID := stringValue(newTab["tab_id"])
	if newTabID == "" {
		t.Fatalf("new tab has no tab_id: %v", newTab)
	}

	tabsOut := sc.MCP("browser_session", map[string]any{
		"action":     "tabs",
		"session_id": sessionID,
	})
	originalTabID := tabIDWithURLPrefix(t, tabsOut, "data:text/html")
	if originalTabID == "" {
		t.Fatalf("could not find original data tab: %v", tabsOut)
	}

	backOut := sc.MCP("browser_session", map[string]any{
		"action":     "switch_tab",
		"session_id": sessionID,
		"tab_id":     originalTabID,
	})
	if got := stringValue(backOut["active_tab_id"]); got != originalTabID {
		t.Fatalf("switch_tab back active_tab_id: want %q, got %q (out=%v)", originalTabID, got, backOut)
	}
	if got := stringValue(backOut["current_url"]); !strings.HasPrefix(got, "data:text/html") {
		t.Fatalf("switch_tab back current_url: want original data URL, got %q (out=%v)", got, backOut)
	}

	shotOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if got := stringValue(shotOut["active_tab_id"]); got != originalTabID {
		t.Fatalf("screenshot after switch should stay on original tab %q, got %q (out=%v)", originalTabID, got, shotOut)
	}

	forwardOut := sc.MCP("browser_session", map[string]any{
		"action":     "switch_tab",
		"session_id": sessionID,
		"tab_id":     newTabID,
	})
	if got := stringValue(forwardOut["active_tab_id"]); got != newTabID {
		t.Fatalf("switch_tab forward active_tab_id: want %q, got %q (out=%v)", newTabID, got, forwardOut)
	}
	if got := stringValue(forwardOut["current_url"]); !strings.Contains(got, "computer-tab-test") {
		t.Fatalf("switch_tab forward current_url: want target page, got %q (out=%v)", got, forwardOut)
	}
}

// TestComputerAppBrowserCloseTab verifies the close_tab action can close a
// page target without hitting chromedp's page-target CloseTarget guard.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserCloseTab -timeout 3m .
func TestComputerAppBrowserCloseTab(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser close-tab test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     newTabFixtureDataURL(),
		"viewport": map[string]any{
			"width":  900,
			"height": 500,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	clickOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": fcoord(110, 92),
	})
	newTabs, _ := clickOut["new_tabs"].([]any)
	if len(newTabs) != 1 {
		t.Fatalf("expected one new tab, got %v", clickOut["new_tabs"])
	}
	newTab, _ := newTabs[0].(map[string]any)
	newTabID := stringValue(newTab["tab_id"])
	if newTabID == "" {
		t.Fatalf("new tab has no tab_id: %v", newTab)
	}

	closeOut := sc.MCP("browser_session", map[string]any{
		"action":     "close_tab",
		"session_id": sessionID,
		"tab_id":     newTabID,
	})
	if got, ok := numericValue(closeOut["tab_count"]); !ok || got != 1 {
		t.Fatalf("after close_tab want tab_count=1, got %v (out=%v)", closeOut["tab_count"], closeOut)
	}
	if got := stringValue(closeOut["active_tab_id"]); got == "" || got == newTabID {
		t.Fatalf("close_tab left active tab on closed tab id %q: %v", newTabID, closeOut)
	}
}

// TestComputerAppBrowserUploadFile proves action=upload_file against a
// real browser backend. It uses a data: page whose file input updates the
// URL hash on change, so the assertion works for both local Chrome and
// Browserbase without exposing a local test server to the cloud browser.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserUploadFile -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserUploadFile -timeout 5m .
func TestComputerAppBrowserUploadFile(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser upload test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}

	pageHTML := `<!doctype html>
<html><body>
<input id="fileUpload" type="file" accept="image/png,image/jpeg">
<div id="status">waiting</div>
<script>
document.getElementById('fileUpload').addEventListener('change', function() {
  var file = this.files && this.files[0];
  document.getElementById('status').textContent = file ? 'uploaded:' + file.name : 'none';
  if (file) location.href = 'about:blank#uploaded-' + encodeURIComponent(file.name);
});
</script>
</body></html>`
	pageURL := "data:text/html;charset=utf-8," + url.PathEscape(pageHTML)

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     pageURL,
		"viewport": map[string]any{
			"width":  1000,
			"height": 700,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "input#fileUpload",
		"filename":   "agent-upload.png",
		"mime_type":  "image/png",
		"base64":     base64.StdEncoding.EncodeToString([]byte("fake png bytes")),
	})
	if out["uploaded"] != true || out["filename"] != "agent-upload.png" {
		t.Fatalf("upload output metadata = %#v", out)
	}
	current, _ := out["current_url"].(string)
	if !strings.Contains(current, "#uploaded-agent-upload.png") {
		t.Fatalf("upload did not trigger page change, current_url=%q out=%#v", current, out)
	}
}

func decodeScreenshot(t *testing.T, out map[string]any) []byte {
	t.Helper()
	b64, _ := out["screenshot_b64"].(string)
	if b64 == "" {
		t.Fatalf("missing screenshot_b64 in result: %v", out)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode screenshot_b64: %v", err)
	}
	return raw
}

func hasBadgeLikePixels(t *testing.T, raw []byte) bool {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode screenshot image: %v", err)
	}
	b := img.Bounds()
	var blue, green, orange int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r, g, bb := int(r16>>8), int(g16>>8), int(b16>>8)
			if bb > 180 && r > 25 && r < 110 && g > 80 && g < 170 {
				blue++
			}
			if g > 150 && r > 10 && r < 90 && bb > 45 && bb < 140 {
				green++
			}
			if r > 190 && g > 70 && g < 150 && bb < 80 {
				orange++
			}
		}
	}
	return blue+green+orange > 30
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func tabIDWithURLPrefix(t *testing.T, out map[string]any, prefix string) string {
	t.Helper()
	tabs, _ := out["tabs"].([]any)
	for _, raw := range tabs {
		tab, _ := raw.(map[string]any)
		if strings.HasPrefix(stringValue(tab["url"]), prefix) {
			return stringValue(tab["tab_id"])
		}
	}
	return ""
}

func shortcutKeysDataURL() string {
	html := `<!doctype html>
<meta charset="utf-8">
<title>Shortcut key test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; }
  label { position: absolute; left: 40px; width: 120px; height: 48px; line-height: 48px; }
  input { position: absolute; left: 180px; width: 300px; font: inherit; padding: 8px; height: 48px; box-sizing: border-box; }
  #a-label, #a { top: 80px; }
  #b-label, #b { top: 160px; }
</style>
<label id="a-label" for="a">First</label><input id="a" value="alpha">
<label id="b-label" for="b">Second</label><input id="b">
<script>
const state = {tab:false, selectAll:false, backspace:false, undo:false};
const a = document.getElementById("a");
const b = document.getElementById("b");
b.addEventListener("focus", () => { state.tab = true; check(); });
a.addEventListener("input", () => {
  if (a.value === "X") state.selectAll = true;
  if (state.selectAll && a.value === "") state.backspace = true;
  if (state.backspace && a.value === "Z") state.typedZ = true;
  if (state.typedZ && a.value === "") state.undo = true;
  check();
});
function check() {
  if (state.tab && state.selectAll && state.backspace && state.undo) {
    location.href = "about:blank#computer_key_test=pass";
  }
}
</script>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}

func temporalInputsHTML() string {
	return `<!doctype html>
<meta charset="utf-8">
<title>Temporal input test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; }
  label { position: absolute; left: 40px; width: 120px; height: 48px; line-height: 48px; }
  input { position: absolute; left: 180px; width: 300px; font: inherit; padding: 8px; height: 48px; box-sizing: border-box; }
  #date-label, #date { top: 80px; }
  #time-label, #time { top: 160px; }
  #datetime-label, #datetime { top: 240px; }
</style>
<label id="date-label" for="date">Date</label><input id="date" type="date">
<label id="time-label" for="time">Time</label><input id="time" type="time">
<label id="datetime-label" for="datetime">Date time</label><input id="datetime" type="datetime-local">
<script>
function sync() {
  const p = new URLSearchParams();
  for (const id of ["date", "time", "datetime"]) p.set(id, document.getElementById(id).value);
  location.hash = p.toString();
}
for (const input of document.querySelectorAll("input")) {
  input.addEventListener("input", sync);
  input.addEventListener("change", sync);
}
sync();
</script>`
}

func newTabFixtureDataURL() string {
	html := `<!doctype html>
<meta charset="utf-8">
<title>New tab test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; padding: 64px; }
  a { display: inline-block; padding: 14px 18px; border: 1px solid #333; border-radius: 4px; color: #111; }
</style>
<a target="_blank" href="https://example.com/#computer-tab-test">Open model detail</a>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func fcoord(x, y int) string {
	return strconv.Itoa(x) + "," + strconv.Itoa(y)
}

func hashValues(rawURL string) map[string]string {
	out := map[string]string{}
	u, err := url.Parse(rawURL)
	if err != nil {
		return out
	}
	q, err := url.ParseQuery(u.Fragment)
	if err != nil {
		return out
	}
	for k, v := range q {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
