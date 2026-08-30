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

func TestLocalMediaOutcomeWaitRecognizesBunnyEmbedAndPatreonRejection(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_OUTCOME_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_OUTCOME_TESTS=1")
	}

	const bunnyURL = "https://iframe.mediadelivery.net/play/374587/dfea9276-4b32-44e2-be3a-87d137752dc6"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<button id="loading">Start loading</button><button id="embed">Embed video</button><button id="reject">Reject video</button><div id="result"></div>
<script>
document.getElementById('loading').addEventListener('click', function () {
  document.getElementById('result').innerHTML = '<figure data-testid="media-embed"><div role="progressbar" aria-label="Loading video">Loading video</div></figure>';
});
document.getElementById('embed').addEventListener('click', function () {
  setTimeout(function () {
    document.getElementById('result').innerHTML = '<figure data-testid="media-embed">' +
      '<img alt="Player thumbnail" style="width:160px;height:90px" src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==">' +
      '<iframe title="Bunny video player" style="width:480px;height:270px" src="` + bunnyURL + `"></iframe>' +
      '<span>03:21</span><button>Media settings</button></figure><div style="position:fixed;top:20px;left:20px">Saved</div>';
  }, 100);
});
document.getElementById('reject').addEventListener('click', function () {
  document.getElementById('result').innerHTML = '<div role="alert">This URL doesn\'t look like a video or audio file</div>';
});
</script></body></html>`))
	}))
	t.Cleanup(server.Close)

	c, err := New(computer.DisplaySize{Width: 900, Height: 700})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.OpenSession(computer.OpenOptions{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	initial, err := c.ObserveMedia()
	if err != nil || initial.EmbedStatus != "unknown" || initial.DraftSaveState != "unknown" {
		t.Fatalf("initial media state: observation=%+v err=%v", initial, err)
	}
	if _, err := c.Execute(computer.Action{Type: "click", Selector: "#loading"}); err != nil {
		t.Fatal(err)
	}
	loading, err := c.ObserveMedia()
	if err != nil || loading.EmbedStatus != "loading" {
		t.Fatalf("loading media state: observation=%+v err=%v", loading, err)
	}
	if _, err := c.Execute(computer.Action{Type: "click", Selector: "#embed"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.WaitForOutcome([]computer.WaitCondition{{Type: "media_present"}, {Type: "media_error"}}, "any", 100, 5000)
	if err != nil {
		t.Fatalf("wait for Bunny media: %v", err)
	}
	if !loaded.Matched || loaded.Media == nil || loaded.Media.EmbedStatus != "loaded" || loaded.Media.Provider != "mediadelivery.net" || loaded.Media.IframeSrc != bunnyURL {
		t.Fatalf("Bunny embed evidence: %+v", loaded)
	}
	if !loaded.Media.PlayerVisible || !loaded.Media.IframeVisible || !loaded.Media.ThumbnailVisible || loaded.Media.DurationText != "03:21" || !loaded.Media.ConfigurationPresent || loaded.Media.DraftSaveState != "saved" {
		t.Fatalf("structured player/draft evidence: %+v", loaded.Media)
	}

	if _, err := c.Execute(computer.Action{Type: "click", Selector: "#reject"}); err != nil {
		t.Fatal(err)
	}
	rejected, err := c.WaitForOutcome([]computer.WaitCondition{{Type: "media_error"}}, "any", 0, 2000)
	if err != nil {
		t.Fatalf("wait for Patreon rejection: %v", err)
	}
	if !rejected.Matched || rejected.Media == nil || rejected.Media.EmbedStatus != "rejected" || rejected.Media.ErrorText != "This URL doesn't look like a video or audio file" {
		t.Fatalf("Patreon rejection evidence: %+v", rejected)
	}
}
