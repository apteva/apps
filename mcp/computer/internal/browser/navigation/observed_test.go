package navigation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestRetryClassificationAndSafeURL(t *testing.T) {
	for _, message := range []string{"net::ERR_HTTP2_SERVER_REFUSED_STREAM", "net::ERR_CONNECTION_CLOSED", "net::ERR_CONNECTION_RESET"} {
		if !Retryable(errors.New(message)) {
			t.Fatal(message)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("net::ERR_ABORTED"), errors.New("WebSocket lost after dispatch"), errors.New("net::ERR_CERT_AUTHORITY_INVALID")} {
		if Retryable(err) {
			t.Fatal("unsafe retry", err)
		}
	}
	if safe := SafeURL("https://user:password@example.com/performers?token=secret#private"); safe != "https://example.com/performers" {
		t.Fatal(safe)
	}
}

func TestObservedNavigationRedirectsAndRedaction(t *testing.T) {
	if os.Getenv("RUN_NAVIGATION_BROWSER_TESTS") != "1" {
		t.Skip("set RUN_NAVIGATION_BROWSER_TESTS=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/performers?token=second-secret", 302)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<body>performers</body>"))
	}))
	defer server.Close()
	alloc, stop := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.UserDataDir(t.TempDir()))...)
	defer stop()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := Observed(ctx, "navigate", server.URL+"/start?token=first-secret", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Requests) != 2 || a.Requests[0].Status != 302 || a.Requests[1].Status != 200 || a.Requests[1].ConnectionReused == nil || a.Requests[1].Protocol == "" {
		t.Fatalf("missing redirect/network evidence: %+v", a)
	}
	encoded, _ := json.Marshal(a)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("query secret leaked: %s", encoded)
	}
}
