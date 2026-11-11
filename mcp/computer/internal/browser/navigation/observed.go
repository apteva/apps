package navigation

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type Failure struct {
	Diagnostics *api.NavigationDiagnostics
	Cause       error
}

func (e *Failure) Error() string { return "navigation_failed: " + e.Cause.Error() }
func (e *Failure) Unwrap() error { return e.Cause }

func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, code := range []string{"net::ERR_HTTP2_SERVER_REFUSED_STREAM", "net::ERR_CONNECTION_CLOSED", "net::ERR_CONNECTION_RESET"} {
		if strings.Contains(err.Error(), code) {
			return true
		}
	}
	return false
}

// SafeURL never retains credentials, query values or fragment secrets in logs.
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	safe := u.String()
	if len(safe) > 2048 {
		safe = safe[:2048]
	}
	return safe
}

// Observed captures top-frame requests only. It neither retries navigation nor
// records headers, cookies or POST bodies. The caller owns the recovery policy.
func Observed(parent context.Context, action, rawURL string, timeout time.Duration) (api.NavigationAttempt, error) {
	a := api.NavigationAttempt{URL: SafeURL(rawURL), Requests: []api.NavigationRequest{}, TransportDetails: "unavailable: standard CDP does not expose HTTP/2 RST_STREAM, GOAWAY or socket close frames"}
	if c := chromedp.FromContext(parent); c != nil && c.Target != nil {
		a.TabID = string(c.Target.TargetID)
	}
	started := time.Now()
	var mu sync.Mutex
	var mainFrame cdp.FrameID
	var requestID network.RequestID
	ctx, stop := context.WithCancel(parent)
	defer stop()
	if err := cdputil.Run(parent, network.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err == nil && tree != nil {
			mainFrame = tree.Frame.ID
		}
		return err
	})); err != nil {
		return a, err
	}
	response := func(r *network.Response) {
		if r == nil || len(a.Requests) == 0 {
			return
		}
		x := &a.Requests[len(a.Requests)-1]
		x.Status = r.Status
		x.Protocol = r.Protocol
		x.ConnectionID = r.ConnectionID
		reused := r.ConnectionReused
		x.ConnectionReused = &reused
		x.RemoteIP = r.RemoteIPAddress
	}
	chromedp.ListenTarget(ctx, func(event any) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		switch e := event.(type) {
		case *network.EventRequestWillBeSent:
			if e.Type != network.ResourceTypeDocument || e.FrameID != mainFrame {
				return
			}
			requestID = e.RequestID
			if e.RedirectResponse != nil {
				response(e.RedirectResponse)
			}
			if len(a.Requests) < 20 {
				a.Requests = append(a.Requests, api.NavigationRequest{URL: SafeURL(e.Request.URL), Method: e.Request.Method})
			}
			if e.Request.Method != "GET" && e.Request.Method != "HEAD" {
				a.UnsafeMethod = true
			}
		case *network.EventResponseReceived:
			if e.RequestID == requestID {
				response(e.Response)
			}
		case *network.EventLoadingFailed:
			if e.RequestID != requestID || e.Type != network.ResourceTypeDocument {
				return
			}
			a.Error = e.ErrorText
			a.Canceled = e.Canceled
			a.BlockedReason = string(e.BlockedReason)
			if len(a.Requests) > 0 {
				a.FailedURL = a.Requests[len(a.Requests)-1].URL
			}
		}
	})
	err := Run(parent, action, rawURL, timeout)
	var current string
	if parent.Err() == nil {
		_ = cdputil.Run(parent, chromedp.Location(&current))
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	a.FinalURL = SafeURL(current)
	a.DurationMS = time.Since(started).Milliseconds()
	if err != nil && a.Error == "" {
		a.Error = strings.ReplaceAll(err.Error(), rawURL, SafeURL(rawURL))
		if len(a.Error) > 512 {
			a.Error = a.Error[:512]
		}
	}
	return a, err
}
