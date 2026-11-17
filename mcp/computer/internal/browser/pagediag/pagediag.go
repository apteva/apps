// Package pagediag records page problems for a browser session from Chrome
// DevTools Protocol events: uncaught exceptions (Runtime.exceptionThrown),
// console/browser errors (Runtime.consoleAPICalled, Log.entryAdded), failed
// requests (Network.loadingFailed), and responses with status >= 400
// (Network.responseReceived).
//
// chromedp already enables the Runtime, Log, and Network domains on every
// target it attaches to, so recording only adds event listeners and issues no
// extra CDP commands that page scripts could observe.
//
// Privacy: request entries keep method, origin, path, and status only; URLs
// inside exception text lose their query strings; console text is counted,
// never stored.
package pagediag

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	MaxExceptions    = 50
	MaxRequests      = 100
	MaxConsoleEvents = 1000
	maxTextChars     = 1000
	maxPathChars     = 300
	maxTracked       = 2000
)

type request struct {
	url    string
	method string
}

type exception struct {
	seq  uint64
	text string
}

type requestEntry struct {
	seq uint64
	computer.PageRequestDiagnostic
}

// Recorder is safe for concurrent use. One Recorder is shared by every tab
// of a session. A nil *Recorder reports diagnostics as disabled.
type Recorder struct {
	mu         sync.Mutex
	seq        uint64
	exceptions []exception
	console    []uint64
	requests   []requestEntry
	truncated  bool
	attached   map[context.Context]struct{}
	inflight   map[network.RequestID]request
	now        func() time.Time
}

func New() *Recorder {
	return &Recorder{
		attached: make(map[context.Context]struct{}),
		inflight: make(map[network.RequestID]request),
		now:      time.Now,
	}
}

// Attach starts recording events from the target bound to ctx. Attaching the
// same context twice is a no-op; a nil Recorder ignores Attach.
func (r *Recorder) Attach(ctx context.Context) {
	if r == nil || ctx == nil {
		return
	}
	r.mu.Lock()
	if _, ok := r.attached[ctx]; ok {
		r.mu.Unlock()
		return
	}
	r.attached[ctx] = struct{}{}
	r.mu.Unlock()
	chromedp.ListenTarget(ctx, r.Observe)
}

// Snapshot returns problems recorded after the since cursor (0 = all kept).
// clear=true discards everything recorded so far, including entries older
// than since, and resets the truncated flag. The cursor keeps increasing
// across clears.
func (r *Recorder) Snapshot(since uint64, clear bool) computer.PageDiagnostics {
	out := computer.PageDiagnostics{Exceptions: []string{}, Requests: []computer.PageRequestDiagnostic{}}
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out.Enabled = true
	out.Cursor = r.seq
	out.Truncated = r.truncated
	for _, e := range r.exceptions {
		if e.seq > since {
			out.Exceptions = append(out.Exceptions, e.text)
		}
	}
	for _, seq := range r.console {
		if seq > since {
			out.ConsoleErrors++
		}
	}
	for _, e := range r.requests {
		if e.seq > since {
			out.Requests = append(out.Requests, e.PageRequestDiagnostic)
		}
	}
	if clear {
		r.exceptions, r.console, r.requests, r.truncated = nil, nil, nil, false
	}
	return out
}

// Observe handles one CDP event. It is exported for tests.
func (r *Recorder) Observe(event any) {
	switch ev := event.(type) {
	case *cdpruntime.EventExceptionThrown:
		if ev.ExceptionDetails == nil {
			return
		}
		d := ev.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = d.Exception.Description
		}
		r.addException(text)
	case *cdpruntime.EventConsoleAPICalled:
		if ev.Type == cdpruntime.APITypeError || ev.Type == cdpruntime.APITypeAssert {
			r.addConsole()
		}
	case *cdplog.EventEntryAdded:
		// Network-sourced log lines ("Failed to load resource: 500") duplicate
		// the request entries recorded below.
		if e := ev.Entry; e != nil && e.Level == cdplog.LevelError && e.Source != cdplog.SourceNetwork {
			r.addConsole()
		}
	case *network.EventRequestWillBeSent:
		if ev.Request == nil {
			return
		}
		r.mu.Lock()
		if len(r.inflight) < maxTracked {
			r.inflight[ev.RequestID] = request{url: ev.Request.URL, method: ev.Request.Method}
		}
		r.mu.Unlock()
	case *network.EventResponseReceived:
		if ev.Response == nil || ev.Response.Status < 400 {
			return
		}
		r.mu.Lock()
		req := r.inflight[ev.RequestID]
		r.mu.Unlock()
		target := ev.Response.URL
		if target == "" {
			target = req.url
		}
		r.addRequest(target, req.method, int(ev.Response.Status), "", string(ev.Type))
	case *network.EventLoadingFinished:
		r.mu.Lock()
		delete(r.inflight, ev.RequestID)
		r.mu.Unlock()
	case *network.EventLoadingFailed:
		r.mu.Lock()
		req := r.inflight[ev.RequestID]
		delete(r.inflight, ev.RequestID)
		r.mu.Unlock()
		// Cancellations are aborts initiated by the page or a navigation, not
		// failures of the request.
		if ev.Canceled || req.url == "" {
			return
		}
		text := ev.ErrorText
		if ev.BlockedReason != "" {
			text = strings.TrimSpace(text + " (blocked: " + string(ev.BlockedReason) + ")")
		}
		r.addRequest(req.url, req.method, 0, text, string(ev.Type))
	}
}

func (r *Recorder) addException(text string) {
	text = clip(stripURLQueries(strings.TrimSpace(text)), maxTextChars)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.exceptions = append(r.exceptions, exception{seq: r.seq, text: text})
	if len(r.exceptions) > MaxExceptions {
		r.exceptions = r.exceptions[1:]
		r.truncated = true
	}
}

func (r *Recorder) addConsole() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.console = append(r.console, r.seq)
	if len(r.console) > MaxConsoleEvents {
		r.console = r.console[1:]
		r.truncated = true
	}
}

func (r *Recorder) addRequest(rawURL, method string, status int, errText, kind string) {
	origin, path, ok := splitURL(rawURL)
	if !ok {
		return
	}
	entry := computer.PageRequestDiagnostic{Method: method, Origin: origin, Path: path, Status: status, Error: clip(errText, 200), Type: strings.ToLower(kind)}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.At = r.now().UTC()
	r.seq++
	r.requests = append(r.requests, requestEntry{seq: r.seq, PageRequestDiagnostic: entry})
	if len(r.requests) > MaxRequests {
		r.requests = r.requests[1:]
		r.truncated = true
	}
}

// splitURL keeps scheme://host[:port] and the path; data:, blob: and other
// non-network URLs are not reported.
func splitURL(raw string) (origin, path string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	switch u.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return "", "", false
	}
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Scheme + "://" + u.Host, clip(path, maxPathChars), true
}

var urlQuery = regexp.MustCompile(`((?:https?|wss?)://[^\s?#)'"]+)[?#][^\s):'"]*`)

func stripURLQueries(text string) string { return urlQuery.ReplaceAllString(text, "$1") }

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
