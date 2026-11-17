package pagediag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestRecorderRedactsBoundsAndClears(t *testing.T) {
	var disabled *Recorder
	if snap := disabled.Snapshot(0, false); snap.Enabled {
		t.Fatal("nil recorder reports disabled")
	}
	r := New()
	r.Observe(&cdplog.EventEntryAdded{Entry: &cdplog.Entry{Level: cdplog.LevelError, Source: cdplog.SourceNetwork, Text: "Failed to load resource"}})
	r.Observe(&cdplog.EventEntryAdded{Entry: &cdplog.Entry{Level: cdplog.LevelWarning, Source: cdplog.SourceJavascript, Text: "warn"}})
	r.Observe(&network.EventRequestWillBeSent{RequestID: "c", Request: &network.Request{URL: "https://app.test/x", Method: "GET"}})
	r.Observe(&network.EventLoadingFailed{RequestID: "c", Canceled: true})
	if got := r.Snapshot(0, false); got.ConsoleErrors != 0 || len(got.Requests) != 0 || got.Cursor != 0 {
		t.Fatalf("network log duplicates, warnings and cancellations are not recorded: %#v", got)
	}

	r.Observe(&network.EventRequestWillBeSent{RequestID: "1", Request: &network.Request{URL: "https://crm.test/api/calls?token=SECRET#frag", Method: "POST"}})
	r.Observe(&network.EventResponseReceived{RequestID: "1", Type: network.ResourceTypeFetch, Response: &network.Response{URL: "https://crm.test/api/calls?token=SECRET", Status: 500}})
	r.Observe(&network.EventRequestWillBeSent{RequestID: "2", Request: &network.Request{URL: "https://cdn.test/a.js?sig=SECRET", Method: "GET"}})
	r.Observe(&network.EventLoadingFailed{RequestID: "2", ErrorText: "net::ERR_NAME_NOT_RESOLVED", Type: network.ResourceTypeScript})
	r.Observe(&cdpruntime.EventExceptionThrown{ExceptionDetails: &cdpruntime.ExceptionDetails{Text: "Uncaught", Exception: &cdpruntime.RemoteObject{Description: "TypeError: x is undefined\n    at https://crm.test/app.js?v=SECRET:4:26"}}})
	r.Observe(&cdpruntime.EventConsoleAPICalled{Type: cdpruntime.APITypeError, Args: []*cdpruntime.RemoteObject{{Type: "string", Value: []byte(`"password SECRET"`)}}})
	snap := r.Snapshot(0, false)
	if fmt.Sprintf("%v", snap) == "" || strings.Contains(fmt.Sprintf("%+v", snap), "SECRET") {
		t.Fatalf("query strings and console text must never be kept: %+v", snap)
	}
	want := computer.PageRequestDiagnostic{Method: "POST", Origin: "https://crm.test", Path: "/api/calls", Status: 500, Type: "fetch"}
	got := snap.Requests[0]
	got.At = time.Time{}
	if got != want {
		t.Fatalf("http error entry: %#v", got)
	}
	if fail := snap.Requests[1]; fail.Status != 0 || fail.Error != "net::ERR_NAME_NOT_RESOLVED" || fail.Path != "/a.js" {
		t.Fatalf("failed request entry: %#v", fail)
	}
	if snap.ConsoleErrors != 1 || len(snap.Exceptions) != 1 || !strings.HasPrefix(snap.Exceptions[0], "TypeError: x is undefined") || !strings.Contains(snap.Exceptions[0], "https://crm.test/app.js:4:26") {
		t.Fatalf("console count / exception text: %#v", snap)
	}

	cursor := snap.Cursor
	r.Observe(&cdpruntime.EventConsoleAPICalled{Type: cdpruntime.APITypeError})
	if newer := r.Snapshot(cursor, false); newer.ConsoleErrors != 1 || len(newer.Requests) != 0 || len(newer.Exceptions) != 0 || newer.Cursor != cursor+1 {
		t.Fatalf("since must return only newer problems: %#v", newer)
	}

	for i := 0; i < MaxRequests+5; i++ {
		id := network.RequestID(fmt.Sprint("r", i))
		r.Observe(&network.EventRequestWillBeSent{RequestID: id, Request: &network.Request{URL: fmt.Sprintf("https://app.test/%d", i), Method: "GET"}})
		r.Observe(&network.EventResponseReceived{RequestID: id, Response: &network.Response{URL: fmt.Sprintf("https://app.test/%d", i), Status: 404}})
	}
	full := r.Snapshot(0, true)
	if len(full.Requests) != MaxRequests || !full.Truncated || full.Requests[MaxRequests-1].Path != fmt.Sprintf("/%d", MaxRequests+4) {
		t.Fatalf("requests must be capped, newest kept, truncated set: len=%d truncated=%v", len(full.Requests), full.Truncated)
	}
	if after := r.Snapshot(0, false); after.ConsoleErrors != 0 || len(after.Requests) != 0 || after.Truncated || after.Cursor != full.Cursor {
		t.Fatalf("clear must empty lists but keep the cursor monotonic: %#v", after)
	}
}

func TestRecorderCapturesExceptionsConsoleAndHTTPErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("real Chromium diagnostics are covered by tier 2")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><script>
console.error("boom from console");
fetch("/api/fail?token=abc").then(() => { document.body.dataset.done = "1" });
setTimeout(() => { throw new Error("kaboom") }, 0);
</script></body></html>`)
		case "/api/fail":
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(ctx); err != nil {
		t.Skipf("chrome unavailable: %v", err)
	}
	r := New()
	r.Attach(ctx)
	r.Attach(ctx) // idempotent: one listener, one entry per event
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible(`body[data-done="1"]`)); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var snap computer.PageDiagnostics
	for time.Now().Before(deadline) {
		snap = r.Snapshot(0, false)
		if len(snap.Exceptions) > 0 && snap.ConsoleErrors > 0 && hasRequest(snap, "/api/fail", 500) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(snap.Exceptions) != 1 || !strings.Contains(snap.Exceptions[0], "kaboom") || snap.ConsoleErrors != 1 || !hasRequest(snap, "/api/fail", 500) {
		t.Fatalf("missing or duplicated diagnostics: %+v", snap)
	}
}

func hasRequest(snap computer.PageDiagnostics, path string, status int) bool {
	n := 0
	for _, req := range snap.Requests {
		if req.Path == path && req.Status == status && req.Method == "GET" && strings.HasPrefix(req.Origin, "http://127.0.0.1:") {
			n++
		}
	}
	return n == 1
}
