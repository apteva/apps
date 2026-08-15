package local

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/stability"
)

// TestLocalOutcomeWaitIgnoresPersistentBackgroundActivity reproduces the
// Patreon save/publish shape in a real browser. The requested outcome is
// observable even though unrelated loading UI, a request, and a frame never
// become globally idle.
func TestLocalOutcomeWaitIgnoresPersistentBackgroundActivity(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_OUTCOME_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_OUTCOME_TESTS=1")
	}

	release := make(chan struct{})
	slowStarted := make(chan struct{}, 1)
	frameStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			select {
			case slowStarted <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-release
			return
		case "/frame":
			select {
			case frameStarted <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-release
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<button id="update">Update</button><p id="status">Editing</p><div id="noise"></div>
<script>
document.getElementById('update').addEventListener('click', function () {
  document.getElementById('status').textContent = 'Saved';
  history.pushState({}, '', '/saved');
  document.getElementById('noise').innerHTML =
    '<span role="progressbar" aria-label="Background sync" aria-busy="true">Saving</span>' +
    '<iframe title="Persistent media player" src="/frame"></iframe>';
  fetch('/slow');
});
</script></body></html>`))
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	c, err := New(computer.DisplaySize{Width: 900, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL + "/edit"}); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if _, err := c.Execute(computer.Action{Type: "click", Selector: "#update"}); err != nil {
		t.Fatalf("click Update: %v", err)
	}
	for name, signal := range map[string]<-chan struct{}{
		"background request": slowStarted,
		"embedded frame":     frameStarted,
	} {
		select {
		case <-signal:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not start", name)
		}
	}

	result, err := c.WaitForOutcome([]computer.WaitCondition{
		{Type: "url_changed", Value: server.URL + "/edit"},
		{Type: "text_present", Value: "Saved"},
	}, "all", 100, 2_000)
	if err != nil {
		t.Fatalf("outcome wait should ignore persistent background activity: %v", err)
	}
	if !result.Matched || result.TimedOut || result.CurrentURL != server.URL+"/saved" {
		t.Fatalf("unexpected outcome result: %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("outcome wait was delayed by unrelated activity: %v", elapsed)
	}

	strict, err := c.WaitForStable(100, 500)
	var timeoutErr *stability.TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("strict stability should return a structured timeout, got result=%+v err=%v", strict, err)
	}
	if !strict.TimedOut || strict.LoadingIndicators < 1 {
		t.Fatalf("strict timeout omitted remaining signals: %+v", strict)
	}
}
