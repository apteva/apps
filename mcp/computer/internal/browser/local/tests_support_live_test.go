package local

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

// TestLocalReadDiagnosticsAndMaskLive exercises the Tests-facing surface
// end-to-end through the real local backend: diagnostics enabled before the
// first page load, read_element on a hidden status marker, and a masked
// capture that restores the page.
func TestLocalReadDiagnosticsAndMaskLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_OUTCOME_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_OUTCOME_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/calls":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "/favicon.ico":
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body>
<div id="fx-api-test-status" hidden>loading</div>
<input id="password" type="password" value="hunter2">
<script>
fetch('/api/calls?token=SECRET').then(() => { document.getElementById('fx-api-test-status').textContent = 'ready' });
setTimeout(() => { undefinedFn() }, 0);
</script></body></html>`)
	}))
	t.Cleanup(server.Close)

	c, err := New(computer.DisplaySize{Width: 900, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.EnablePageDiagnostics()
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitForOutcome([]computer.WaitCondition{{Type: "selector_text", Selector: "#fx-api-test-status", Value: "ready", Op: "equals"}}, "any", 0, 5000); err != nil {
		t.Fatalf("selector_text wait: %v", err)
	}
	status, err := c.ReadElement("#fx-api-test-status", computer.ElementReadOptions{})
	if err != nil || status.Count != 1 || status.Elements[0].Text != "ready" || status.Elements[0].Visible {
		t.Fatalf("read hidden status: %#v %v", status, err)
	}
	pw, err := c.ReadElement("#password", computer.ElementReadOptions{})
	if err != nil || pw.Elements[0].Value != nil || !pw.Elements[0].ValueRedacted {
		t.Fatalf("password must not be read: %#v %v", pw, err)
	}

	var diag computer.PageDiagnostics
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		diag = c.PageDiagnostics(0, false)
		if len(diag.Exceptions) > 0 && len(diag.Requests) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !diag.Enabled || len(diag.Exceptions) != 1 || !strings.Contains(diag.Exceptions[0], "undefinedFn") ||
		len(diag.Requests) != 1 || diag.Requests[0].Path != "/api/calls" || diag.Requests[0].Status != 500 ||
		strings.Contains(fmt.Sprintf("%+v", diag), "SECRET") {
		t.Fatalf("diagnostics from the initial page load: %+v", diag)
	}

	restore, err := c.MaskForScreenshot(computer.ScreenshotMask{Secrets: []string{"hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	if shot, err := c.Screenshot(); err != nil || len(shot) == 0 {
		t.Fatalf("masked capture: %v", err)
	}
	restore()
	after, _ := c.ReadElement("#password", computer.ElementReadOptions{})
	if _, stillMarked := after.Elements[0].Attributes["data-apteva-screenshot-mask"]; stillMarked || !after.Elements[0].Visible {
		t.Fatalf("mask not restored: %#v", after)
	}
}
