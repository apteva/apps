package main

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
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
