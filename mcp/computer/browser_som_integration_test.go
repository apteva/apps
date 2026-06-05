package main

import (
	"bytes"
	"encoding/base64"
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
