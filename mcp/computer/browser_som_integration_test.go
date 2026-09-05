package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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

// TestComputerAppBrowserSetCheckedAndTemporal verifies targeted DOM state
// actions that avoid blind checkbox clicks and fragile focused-field typing.
// It defaults to local and can also run against Browserbase because the
// fixture is a data: URL.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserSetCheckedAndTemporal -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserSetCheckedAndTemporal -timeout 5m .
func TestComputerAppBrowserSetCheckedAndTemporal(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser checked/temporal test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}
	runComputerAppBrowserSetCheckedAndTemporal(t, backend)
}

// TestComputerAppBrowserbaseSetCheckedAndTemporal is the Browserbase-specific
// release regression for the Patreon-like checkbox/switch and date/time path.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbaseSetCheckedAndTemporal -timeout 5m .
func TestComputerAppBrowserbaseSetCheckedAndTemporal(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase checked/temporal test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}
	runComputerAppBrowserSetCheckedAndTemporal(t, "browserbase")
}

// TestComputerAppBrowserbasePublicDatePickerSetTemporal verifies set_temporal
// on a real public page with no iframe. Selenium's web-form demo uses a
// Bootstrap datepicker backed by a plain text input, similar to many scheduler
// widgets where typing focus can be flaky but direct value setting should work.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbasePublicDatePickerSetTemporal -timeout 5m .
func TestComputerAppBrowserbasePublicDatePickerSetTemporal(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase public date picker test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": "browserbase",
		"url":     "https://www.selenium.dev/selenium/web/web-form.html",
		"viewport": map[string]any{
			"width":  1200,
			"height": 900,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_temporal",
		"selector":   "input[name='my-date']",
		"value":      "07/01/2026",
	})
	if got := stringValue(out["temporal_value"]); got != "07/01/2026" {
		t.Fatalf("public date picker temporal_value: want 07/01/2026, got %q out=%v", got, out)
	}
}

// TestComputerAppBrowserbasePublicSetText verifies set_text against a stable
// public non-iframe textarea on Selenium's automation test page.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbasePublicSetText -timeout 5m .
func TestComputerAppBrowserbasePublicSetText(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase public set_text test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": "browserbase",
		"url":     "https://www.selenium.dev/selenium/web/web-form.html",
		"viewport": map[string]any{
			"width":  1200,
			"height": 900,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id":   sessionID,
		"action":       "set_text",
		"selector":     "textarea[name='my-textarea']",
		"text":         "Hello,\n\nThis public textarea should not have a blank paragraph gap.",
		"newline_mode": "compact",
	})
	if got := stringValue(out["text_value"]); got != "Hello,\nThis public textarea should not have a blank paragraph gap." {
		t.Fatalf("public set_text text_value: got %q out=%v", got, out)
	}
	if got := stringValue(out["text_input_type"]); got != "textarea" {
		t.Fatalf("public set_text input type: want textarea, got %q out=%v", got, out)
	}
}

// TestComputerAppBrowserSetText verifies targeted text setting for plain
// textareas and contenteditable composers. It defaults to local and can also
// run against Browserbase because the fixture is a data: URL.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserSetText -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserSetText -timeout 5m .
func TestComputerAppBrowserSetText(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser set_text test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}
	runComputerAppBrowserSetText(t, backend)
}

// TestComputerAppBrowserbaseSetText is the Browserbase-specific release
// regression for composer text replacement and compact newline handling.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbaseSetText -timeout 5m .
func TestComputerAppBrowserbaseSetText(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase set_text test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}
	runComputerAppBrowserSetText(t, "browserbase")
}

func runComputerAppBrowserSetCheckedAndTemporal(t *testing.T, backend string) {
	t.Helper()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     checkedTemporalFixtureDataURL(),
		"viewport": map[string]any{
			"width":  1000,
			"height": 700,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_checked",
		"selector":   "#sell-post",
		"checked":    false,
	})
	if out["checked"] != false {
		t.Fatalf("native checkbox: want checked=false, got %v out=%v", out["checked"], out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_checked",
		"selector":   "#paid-members",
		"checked":    true,
	})
	if out["checked"] != true {
		t.Fatalf("ARIA switch: want checked=true, got %v out=%v", out["checked"], out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_temporal",
		"selector":   "#schedule-date",
		"value":      "2026-07-01",
	})
	if got := stringValue(out["temporal_value"]); got != "2026-07-01" {
		t.Fatalf("date temporal_value: want 2026-07-01, got %q out=%v", got, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_temporal",
		"selector":   "#schedule-time",
		"value":      "11:00 AM",
	})
	if got := stringValue(out["temporal_value"]); got != "11:00" {
		t.Fatalf("time temporal_value: want 11:00, got %q out=%v", got, out)
	}
}

func runComputerAppBrowserSetText(t *testing.T, backend string) {
	t.Helper()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     setTextFixtureDataURL(),
		"viewport": map[string]any{
			"width":  1000,
			"height": 700,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id":   sessionID,
		"action":       "set_text",
		"selector":     "#message",
		"text":         "Hello,\n\nLine two.",
		"newline_mode": "compact",
	})
	if got := stringValue(out["text_value"]); got != "Hello,\nLine two." {
		t.Fatalf("textarea text_value: want compact newline, got %q out=%v", got, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_text",
		"selector":   "#editor",
		"text":       "First paragraph.\n\nSecond paragraph.",
	})
	if got := stringValue(out["text_value"]); got != "First paragraph.\n\nSecond paragraph." {
		t.Fatalf("contenteditable text_value: want preserved blank line, got %q out=%v", got, out)
	}
	if got := stringValue(out["text_verification"]); got != "paragraphs_stable" {
		t.Fatalf("contenteditable verification: want paragraphs_stable, got %q out=%v", got, out)
	}
	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_text",
		"selector":   "#protected-status",
		"text":       "-checked",
		"mode":       "append",
	})
	if got := stringValue(out["text_previous_value"]); got != "pass" {
		t.Fatalf("contenteditable replacement removed protected widget: status=%q out=%v", got, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "set_text",
		"selector":   "#message",
		"text":       "\nAppended.",
		"mode":       "append",
	})
	if got := stringValue(out["text_value"]); got != "Hello,\nLine two.\nAppended." {
		t.Fatalf("append text_value: got %q out=%v", got, out)
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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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

// TestComputerAppBrowserSelectOption verifies the higher-level
// select_option action against native selects, native multiselects, and a
// Patreon-style ARIA combobox. It defaults to local and can also run against
// Browserbase because the fixture is a data: URL.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserSelectOption -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserSelectOption -timeout 5m .
func TestComputerAppBrowserSelectOption(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1 to run the real browser select_option test")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("COMPUTER_APP_BROWSER_BACKEND=browserbase requires BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID")
	}
	runComputerAppBrowserSelectOption(t, backend)
}

// TestComputerAppBrowserbaseSelectOption is the provider-specific regression
// for select_option over Browserbase/CDP. It is separate from the backend-
// parameterized test above so CI and release checks can target Browserbase
// explicitly without relying on COMPUTER_APP_BROWSER_BACKEND.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbaseSelectOption -timeout 5m .
func TestComputerAppBrowserbaseSelectOption(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase select_option test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}
	runComputerAppBrowserSelectOption(t, "browserbase")
}

type comboboxLLMAssessment struct {
	Decision        string `json:"decision"`
	RequestedOption string `json:"requested_option"`
	CurrentValue    string `json:"current_value"`
	Reason          string `json:"reason"`
}

// TestLLMCustomComboboxUnavailableLive proves that a real model can use the
// typed custom-combobox failure to stop instead of retrying or inventing a
// hidden Monthly option when the open menu exposes only Daily.
func TestLLMCustomComboboxUnavailableLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath(computerLLMBinary()); err != nil {
		t.Skip("codex CLI is required")
	}
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action": "open", "backend": "local", "url": selectOptionFixtureDataURL(),
		"viewport": map[string]any{"width": 1000, "height": 700},
	})
	sessionID := stringValue(open["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})
	failure := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "select_option",
		"selector": "#aggregation-combobox", "text": "Monthly",
	})
	shot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	prompt := fmt.Sprintf(`You requested Monthly from an aggregation combobox. Decide whether to retry that option or stop and report it unavailable. Trust the typed Computer result and current semantic state; do not invent hidden options, use raw coordinates, or close the menu.
Computer result: %s
Current SoM: %s`, mustJSON(failure), mustJSON(shot["som"]))
	var assessment comboboxLLMAssessment
	callComputerLLM(t, decodeScreenshot(t, shot), prompt,
		`{"type":"object","additionalProperties":false,"properties":{"decision":{"type":"string","enum":["stop","retry"]},"requested_option":{"type":"string"},"current_value":{"type":"string"},"reason":{"type":"string"}},"required":["decision","requested_option","current_value","reason"]}`,
		&assessment)
	if assessment.Decision != "stop" || assessment.RequestedOption != "Monthly" || assessment.CurrentValue != "Daily" {
		t.Fatalf("model did not use typed combobox state: %+v failure=%v", assessment, failure)
	}
	t.Logf("LLM respected unavailable custom option: %+v", assessment)
}

// TestComputerAppBrowserbasePublicMultiSelectOption verifies select_option on
// a public WAI-ARIA APG multiselect listbox. This keeps coverage for real
// role=listbox/aria-multiselectable markup, not just the local fixture.
//
//	RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbasePublicMultiSelectOption -timeout 5m .
func TestComputerAppBrowserbasePublicMultiSelectOption(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1 to run the Browserbase public multiselect test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": "browserbase",
		"url":     "https://www.w3.org/WAI/ARIA/apg/patterns/listbox/examples/listbox-rearrangeable/",
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "#ms_imp_list",
		"texts":      []any{"Leather seats", "Food synthesizer"},
		"mode":       "replace",
	})
	selected := stringSliceValue(out["select_selected"])
	if !containsString(selected, "Leather seats") || !containsString(selected, "Food synthesizer") {
		t.Fatalf("public ARIA multiselect: want selected Leather seats and Food synthesizer, got %v out=%v", selected, out)
	}
}

func runComputerAppBrowserSelectOption(t *testing.T, backend string) {
	t.Helper()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": backend,
		"url":     selectOptionFixtureDataURL(),
		"viewport": map[string]any{
			"width":  1000,
			"height": 700,
		},
	})
	sessionID, _ := open["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "#tier",
		"value":      "pro",
	})
	if got := stringSliceValue(out["select_selected"]); !containsString(got, "Pro") {
		t.Fatalf("native select tier: want selected Pro, got %v out=%v", got, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "#colors",
		"texts":      []any{"Red", "Blue"},
		"mode":       "replace",
	})
	selected := stringSliceValue(out["select_selected"])
	if !containsString(selected, "Red") || !containsString(selected, "Blue") {
		t.Fatalf("native multiselect colors: want selected Red and Blue, got %v out=%v", selected, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "#tier-combobox",
		"text":       "VIP",
	})
	if got := stringSliceValue(out["select_matched"]); !containsString(got, "VIP") {
		t.Fatalf("custom combobox: want matched VIP, got %v out=%v", got, out)
	}
	if got := stringValue(out["select_control_text"]); got != "VIP" {
		t.Fatalf("custom combobox: want control text VIP, got %q out=%v", got, out)
	}

	out = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "#aggregation-combobox",
		"text":       "Monthly",
	})
	if boolFromAny(out["success"]) || stringValue(out["error_code"]) != "custom_combobox_option_unavailable" {
		t.Fatalf("unavailable custom option was not typed: %v", out)
	}
	if stringValue(out["control_kind"]) != "button_combobox" || !boolFromAny(out["menu_open"]) || boolFromAny(out["option_available"]) {
		t.Fatalf("custom combobox state incomplete: %v", out)
	}
	if stringValue(out["requested_option"]) != "Monthly" || stringValue(out["current_value"]) != "Daily" || !containsString(stringSliceValue(out["visible_options"]), "Daily") {
		t.Fatalf("custom combobox option inventory incomplete: %v", out)
	}
	if revision, ok := numericValue(out["som_revision"]); !ok || revision < 1 {
		t.Fatalf("custom combobox failure omitted semantic revision: %v", out)
	}
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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

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

// TestComputerAppBrowserbasePublicUploadFromURL is an opt-in paid/live test
// for the production Browserbase path: Computer downloads a public file URL,
// uploads it into a real public file input, submits the real public form, and
// verifies the resulting page text from the returned screenshot.
//
//	RUN_COMPUTER_APP_PUBLIC_UPLOAD_TESTS=1 BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserbasePublicUploadFromURL -timeout 5m .
func TestComputerAppBrowserbasePublicUploadFromURL(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_PUBLIC_UPLOAD_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_PUBLIC_UPLOAD_TESTS=1 to run the public Browserbase upload test")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID are required")
	}

	const (
		uploadPage = "https://the-internet.herokuapp.com/upload"
		sourceURL  = "https://the-internet.herokuapp.com/img/forkme_right_green_007200.png"
		filename   = "apteva-public-upload.png"
	)

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	open := sc.MCP("browser_session", map[string]any{
		"action":  "open",
		"backend": "browserbase",
		"url":     uploadPage,
		"viewport": map[string]any{
			"width":  1200,
			"height": 800,
		},
	})
	sessionID, _ := open["session_id"].(string)
	providerSessionID, _ := open["backend_session_id"].(string)
	if sessionID == "" || providerSessionID == "" {
		t.Fatalf("open returned incomplete session ids: %v", open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	out := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "#file-upload",
		"source_url": sourceURL,
		"filename":   filename,
		"mime_type":  "image/png",
	})
	if out["uploaded"] != true || out["filename"] != filename || out["file_source"] != "source_url" {
		t.Fatalf("upload metadata = %#v", out)
	}

	_ = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      2,
	})
	final := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID,
		"action":     "wait",
		"duration":   1000,
	})
	ocr := ocrScreenshotText(t, final)
	if !strings.Contains(ocr, "File Uploaded") || !strings.Contains(ocr, filename) {
		t.Fatalf("upload success text not found in screenshot OCR; text=%q", ocr)
	}
}

func ocrScreenshotText(t *testing.T, out map[string]any) string {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract is required for screenshot OCR verification")
	}
	pngPath := filepath.Join(t.TempDir(), "browserbase-public-upload.png")
	if err := os.WriteFile(pngPath, decodeScreenshot(t, out), 0o600); err != nil {
		t.Fatalf("write OCR screenshot: %v", err)
	}
	raw, err := exec.Command("tesseract", pngPath, "stdout").CombinedOutput()
	if err != nil {
		t.Fatalf("tesseract OCR failed: %v output=%s", err, string(raw))
	}
	t.Logf("OCR screenshot saved to %s", pngPath)
	return string(raw)
}

func decodeScreenshot(t *testing.T, out map[string]any) []byte {
	t.Helper()
	envelope, _ := out["screenshot"].(map[string]any)
	b64, _ := envelope["base64"].(string)
	if b64 == "" {
		t.Fatalf("missing screenshot envelope in result: %v", out)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode screenshot envelope: %v", err)
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

func checkedTemporalFixtureDataURL() string {
	html := `<!doctype html>
<meta charset="utf-8">
<title>Checked and temporal test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; padding: 40px; }
  label, .row { display: block; margin: 18px 0 6px; }
  input, button { font: inherit; padding: 8px 12px; min-width: 220px; }
  [role=switch] { border: 1px solid #333; background: #eee; border-radius: 4px; }
  [role=switch][aria-checked=true] { background: #cdeafe; }
</style>
<label><input id="sell-post" type="checkbox" checked> Sell this post</label>
<button id="paid-members" role="switch" aria-checked="false" type="button">Paid members</button>
<label for="schedule-date">Date</label><input id="schedule-date" type="date">
<label for="schedule-time">Time</label><input id="schedule-time" type="time">
<script>
const sell = document.getElementById("sell-post");
const paid = document.getElementById("paid-members");
const date = document.getElementById("schedule-date");
const time = document.getElementById("schedule-time");
paid.addEventListener("click", () => {
  const next = paid.getAttribute("aria-checked") !== "true";
  paid.setAttribute("aria-checked", String(next));
  sync();
});
function sync() {
  const p = new URLSearchParams();
  p.set("sell", String(sell.checked));
  p.set("paid", String(paid.getAttribute("aria-checked") === "true"));
  p.set("date", date.value);
  p.set("time", time.value);
  location.hash = p.toString();
}
for (const input of [sell, date, time]) {
  input.addEventListener("input", sync);
  input.addEventListener("change", sync);
}
sync();
</script>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}

func setTextFixtureDataURL() string {
	html := `<!doctype html>
<meta charset="utf-8">
<title>Set text test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; padding: 40px; }
  label { display: block; margin: 18px 0 6px; }
  textarea, [contenteditable] { display: block; width: 620px; min-height: 120px; font: inherit; padding: 8px 12px; border: 1px solid #777; white-space: pre-wrap; }
</style>
<label for="message">Message</label>
<textarea id="message"></textarea>
<label for="editor">Composer</label>
<div id="editor" role="textbox" contenteditable="true" aria-label="Composer"><div id="paywall" contenteditable="false">Paid access starts here</div><p>Original controlled value.</p></div>
<input id="protected-status" aria-label="Protected widget status" value="pass">
<script>
const message = document.getElementById("message");
const editor = document.getElementById("editor");
const protectedStatus = document.getElementById("protected-status");
// Model the reconciliation behavior of ProseMirror/Remirror. Browser-native
// edits generate a trusted input event and update editor state. Direct DOM
// replacement followed by a synthetic input event is rejected and restored.
let acceptedEditorHTML = editor.innerHTML;
let reconciling = false;
editor.addEventListener("input", event => {
  if (event.isTrusted) acceptedEditorHTML = editor.innerHTML;
});
new MutationObserver(() => setTimeout(() => {
  if (reconciling || editor.innerHTML === acceptedEditorHTML) return;
  reconciling = true;
  editor.innerHTML = acceptedEditorHTML;
  reconciling = false;
  sync();
}, 20)).observe(editor, {subtree: true, childList: true, characterData: true});
function sync() {
  const p = new URLSearchParams();
  p.set("message", message.value);
  p.set("editor", editor.innerText);
  protectedStatus.value = editor.querySelector("#paywall[contenteditable=false]") ? "pass" : "fail";
  p.set("protected", protectedStatus.value);
  location.hash = p.toString();
}
for (const el of [message, editor]) {
  el.addEventListener("input", sync);
  el.addEventListener("change", sync);
}
sync();
</script>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}

func selectOptionFixtureDataURL() string {
	html := `<!doctype html>
<meta charset="utf-8">
<title>Select option test</title>
<style>
  body { font: 20px system-ui, sans-serif; margin: 0; padding: 40px; }
  label { display: block; margin: 18px 0 6px; }
  select, button { font: inherit; padding: 8px 12px; min-width: 240px; }
  #tier-listbox, #aggregation-listbox { display: none; position: absolute; left: 40px; padding: 8px; border: 1px solid #333; background: white; }
  #tier-listbox { top: 300px; }
  #aggregation-listbox { top: 410px; }
  #tier-listbox.open, #aggregation-listbox.open { display: block; }
  [role=option] { padding: 8px 12px; cursor: pointer; }
  [role=option][aria-selected=true] { background: #cdeafe; }
</style>
<label for="tier">Tier</label>
<select id="tier">
  <option value="">All tiers</option>
  <option value="free">Free</option>
  <option value="pro">Pro</option>
  <option value="vip">VIP</option>
</select>
<label for="colors">Colors</label>
<select id="colors" multiple size="4">
  <option value="red">Red</option>
  <option value="green">Green</option>
  <option value="blue">Blue</option>
  <option value="gold">Gold</option>
</select>
<label>Patreon style</label>
<button id="tier-combobox" type="button" role="combobox" aria-label="Select tiers" aria-expanded="false" aria-haspopup="listbox">All tiers</button>
<div id="tier-listbox" role="listbox">
  <div role="option" data-value="all" aria-selected="false">All tiers</div>
  <div role="option" data-value="members" aria-selected="false">Paid members</div>
  <div role="option" data-value="vip" aria-selected="false">VIP</div>
</div>
<label>Aggregation period</label>
<button id="aggregation-combobox" type="button" role="combobox" aria-label="Dropdown to select aggregation period" aria-expanded="false" aria-haspopup="listbox">Daily</button>
<div id="aggregation-listbox" role="listbox">
  <div role="option" data-value="daily" aria-selected="true">Daily</div>
</div>
<script>
const tier = document.getElementById("tier");
const colors = document.getElementById("colors");
const combo = document.getElementById("tier-combobox");
const list = document.getElementById("tier-listbox");
const aggregation = document.getElementById("aggregation-combobox");
const aggregationList = document.getElementById("aggregation-listbox");
function sync(extra) {
  const p = new URLSearchParams(location.hash.slice(1));
  p.set("tier", tier.value);
  p.set("colors", Array.from(colors.selectedOptions).map(o => o.value).join(","));
  if (extra) for (const [k, v] of Object.entries(extra)) p.set(k, v);
  location.hash = p.toString();
}
tier.addEventListener("change", () => sync());
colors.addEventListener("change", () => sync());
combo.addEventListener("click", () => {
  const open = combo.getAttribute("aria-expanded") === "true";
  combo.setAttribute("aria-expanded", open ? "false" : "true");
  list.classList.toggle("open", !open);
});
aggregation.addEventListener("click", () => {
  const open = aggregation.getAttribute("aria-expanded") === "true";
  aggregation.setAttribute("aria-expanded", open ? "false" : "true");
  aggregationList.classList.toggle("open", !open);
});
for (const opt of list.querySelectorAll("[role=option]")) {
  opt.addEventListener("click", () => {
    for (const other of list.querySelectorAll("[role=option]")) other.setAttribute("aria-selected", "false");
    opt.setAttribute("aria-selected", "true");
    combo.textContent = opt.textContent;
    combo.setAttribute("aria-expanded", "false");
    list.classList.remove("open");
    sync({custom: opt.textContent.trim()});
  });
}
sync();
</script>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
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

func stringSliceValue(v any) []string {
	switch vv := v.(type) {
	case []string:
		return append([]string(nil), vv...)
	case []any:
		out := make([]string, 0, len(vv))
		for _, item := range vv {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
