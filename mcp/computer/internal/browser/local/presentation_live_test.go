package local

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/chromedp"
)

func TestLocalPresentationModeLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_PRESENTATION_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_PRESENTATION_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html>
<style>
body { margin: 0; background: #10131a; color: white; font: 20px sans-serif; }
input { position: fixed; left: 100px; top: 100px; width: 240px; height: 44px; font-size: 20px; }
</style>
<input id="demo" aria-label="Demo text">`))
	}))
	defer server.Close()

	c, err := New(computer.DisplaySize{Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	options := computer.PresentationOptions{
		Mode:              "demo",
		ShowCursor:        true,
		TypingDelayMS:     5,
		PointerDurationMS: 20,
		ClickEffectMS:     50,
		PostActionDelayMS: 20,
	}
	if _, err := c.Execute(computer.Action{
		Type:         "click",
		X:            220,
		Y:            122,
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{
		Type:         "type",
		Text:         "visible",
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}

	var state struct {
		Cursor bool   `json:"cursor"`
		Value  string `json:"value"`
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`({
		cursor: !!document.getElementById("__apteva_demo_cursor"),
		value: document.getElementById("demo").value
	})`, &state)); err != nil {
		t.Fatal(err)
	}
	if !state.Cursor || state.Value != "visible" {
		t.Fatalf("presentation state: %+v", state)
	}
	shot, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: false})
	if err != nil || len(shot) == 0 {
		t.Fatalf("presentation screenshot: bytes=%d err=%v", len(shot), err)
	}
}
