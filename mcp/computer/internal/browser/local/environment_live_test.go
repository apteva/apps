package local

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestLocalSessionEnvironmentLive proves the environment is visible on the
// first requested page, including its HTTP request, rather than only after a
// screenshot or later agent action.
func TestLocalSessionEnvironmentLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_ENVIRONMENT_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_ENVIRONMENT_TESTS=1")
	}
	var mu sync.Mutex
	var requestUA, requestLanguage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestUA = r.Header.Get("User-Agent")
		requestLanguage = r.Header.Get("Accept-Language")
		mu.Unlock()
		_, _ = w.Write([]byte(`<html><body>environment ready</body></html>`))
	}))
	defer server.Close()

	dsf := 2.0
	mobile, touch := true, true
	maxTouch := 3
	lat, lon, accuracy := 52.52, 13.405, 25.0
	c, err := New(computer.DisplaySize{Width: 390, Height: 844})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.OpenSession(computer.OpenOptions{
		URL: server.URL,
		Environment: computer.EnvironmentOptions{
			UserAgent: "Computer-Environment-Test/1.0",
			Locale:    "de-DE", Languages: []string{"de-DE", "de", "en"},
			Timezone: "Europe/Berlin",
			Geolocation: &computer.GeolocationOptions{
				Latitude: &lat, Longitude: &lon, Accuracy: &accuracy, Permission: "grant",
			},
			DeviceScaleFactor: &dsf, Mobile: &mobile, Touch: &touch, MaxTouchPoints: &maxTouch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	gotUA, gotLanguage := requestUA, requestLanguage
	mu.Unlock()
	if gotUA != "Computer-Environment-Test/1.0" || !strings.Contains(gotLanguage, "de-DE") {
		t.Fatalf("first request environment: user-agent=%q accept-language=%q", gotUA, gotLanguage)
	}

	var got struct {
		UA          string   `json:"ua"`
		Language    string   `json:"language"`
		Languages   []string `json:"languages"`
		Timezone    string   `json:"timezone"`
		DPR         float64  `json:"dpr"`
		TouchPoints int      `json:"touchPoints"`
		Latitude    float64  `json:"latitude"`
		Longitude   float64  `json:"longitude"`
	}
	script := `new Promise((resolve, reject) => navigator.geolocation.getCurrentPosition(
		position => resolve({
			ua: navigator.userAgent,
			language: navigator.language,
			languages: Array.from(navigator.languages),
			timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
			dpr: devicePixelRatio,
			touchPoints: navigator.maxTouchPoints,
			latitude: position.coords.latitude,
			longitude: position.coords.longitude
		}), reject))`
	awaitPromise := func(params *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
		return params.WithAwaitPromise(true)
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(script, &got, awaitPromise)); err != nil {
		t.Fatal(err)
	}
	if got.UA != "Computer-Environment-Test/1.0" || got.Language != "de-DE" || got.Timezone != "Europe/Berlin" {
		t.Fatalf("browser identity mismatch: %+v", got)
	}
	if got.DPR != 2 || got.TouchPoints != 3 || got.Latitude != lat || got.Longitude != lon {
		t.Fatalf("browser emulation mismatch: %+v", got)
	}
}
