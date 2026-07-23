package local

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
		if r.URL.Path == "/next" {
			_, _ = w.Write([]byte(`<!doctype html><title>Next</title><main id="next">Navigation still works</main>`))
			return
		}
		_, _ = w.Write([]byte(`<!doctype html>
<style>
body { margin: 0; background: #10131a; color: white; font: 19px Georgia, serif; }
#demo { position: fixed; left: 100px; top: 100px; width: 240px; height: 44px; font: 20px monospace; }
#choice { position:fixed; left:100px; top:180px; width:240px; height:44px; font:17px fantasy; }
#agree { position:fixed; left:110px; top:260px; width:28px; height:28px; }
input[data-control="date"] { position:fixed; left:100px; top:330px; width:240px; height:44px; }
#file { position:fixed; left:100px; top:400px; width:280px; }
#next-link { position:fixed; left:430px; top:100px; width:180px; height:44px; }
</style>
<input id="demo" aria-label="Demo text">
<select id="choice"><option value="one">One</option><option value="two">Two</option></select>
<input id="agree" type="checkbox" aria-label="Agree">
<input type="date" data-control="date" aria-label="Date">
<input id="file" type="file" aria-label="File">
<a id="next-link" href="/next">Next page</a>`))
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
	fast := computer.PresentationOptions{Mode: "fast"}
	if _, err := c.Execute(computer.Action{
		Type:         "set_text",
		Selector:     "#demo",
		Text:         "fast",
		Presentation: fast,
	}); err != nil {
		t.Fatal(err)
	}
	var fastOverlay bool
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(
		`!!document.querySelector("[data-apteva-presentation]")`,
		&fastOverlay,
	)); err != nil {
		t.Fatal(err)
	}
	if fastOverlay {
		t.Fatal("fast mode unexpectedly injected a presentation overlay")
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
		Type:         "set_text",
		Selector:     "#demo",
		Text:         "visible",
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{
		Type:         "select_option",
		Selector:     "#choice",
		Value:        "two",
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{
		Type:         "set_checked",
		Selector:     "#agree",
		Checked:      true,
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{
		Type:         "set_temporal",
		Selector:     `input[data-control="date"]`,
		Value:        "2026-07-23",
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}
	var dateCueOnTarget bool
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`(function() {
		const cursor = document.getElementById("__apteva_demo_cursor");
		const target = document.querySelector('input[data-control="date"]');
		if (!cursor || !target) return false;
		const cursorX = parseFloat(cursor.style.left);
		const cursorY = parseFloat(cursor.style.top);
		const rect = target.getBoundingClientRect();
		return Math.abs(cursorX - (rect.left + rect.width / 2)) <= 2 &&
			Math.abs(cursorY - (rect.top + rect.height / 2)) <= 2;
	})()`, &dateCueOnTarget)); err != nil {
		t.Fatal(err)
	}
	if !dateCueOnTarget {
		t.Fatal("presentation cue did not target the repeated id-less date input")
	}
	upload, err := os.CreateTemp(t.TempDir(), "presentation-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := upload.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(computer.Action{
		Type:         "upload_file",
		Selector:     "#file",
		Files:        []string{upload.Name()},
		Presentation: options,
	}); err != nil {
		t.Fatal(err)
	}

	var state struct {
		Cursor    bool   `json:"cursor"`
		Caption   string `json:"caption"`
		Value     string `json:"value"`
		Choice    string `json:"choice"`
		Checked   bool   `json:"checked"`
		Date      string `json:"date"`
		FileCount int    `json:"fileCount"`
	}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(`({
		cursor: !!document.getElementById("__apteva_demo_cursor"),
		caption: document.getElementById("__apteva_demo_caption")?.textContent || "",
		value: document.getElementById("demo").value,
		choice: document.getElementById("choice").value,
		checked: document.getElementById("agree").checked,
		date: document.querySelector('input[data-control="date"]').value,
		fileCount: document.getElementById("file").files.length
	})`, &state)); err != nil {
		t.Fatal(err)
	}
	if !state.Cursor || state.Caption != "File uploaded" || state.Value != "visible" ||
		state.Choice != "two" || !state.Checked || state.Date != "2026-07-23" ||
		state.FileCount != 1 {
		t.Fatalf("presentation state: %+v", state)
	}
	shot, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: false})
	if err != nil || len(shot) == 0 {
		t.Fatalf("presentation screenshot: bytes=%d err=%v", len(shot), err)
	}

	if _, err := c.Execute(computer.Action{
		Type:         "click",
		X:            500,
		Y:            122,
		Presentation: options,
	}); err != nil {
		t.Fatalf("demo click navigation: %v", err)
	}
	current, err := url.Parse(c.CurrentURL())
	if err != nil {
		t.Fatal(err)
	}
	if current.Path != "/next" {
		t.Fatalf("presentation layer changed click navigation: url=%q", c.CurrentURL())
	}
}
