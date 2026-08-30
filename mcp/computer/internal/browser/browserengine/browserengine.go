// Package browserengine implements the Computer interface using the
// Browser Engine API (api.browserengine.co) — our hosted wrapper
// around services/browser-service. Uses CDP-over-WebSocket, same
// shape as Browserbase and Steel.
package browserengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdptabs"
	"github.com/apteva/apps/mcp/computer/internal/browser/clickguard"
	"github.com/apteva/apps/mcp/computer/internal/browser/domextract"
	"github.com/apteva/apps/mcp/computer/internal/browser/environment"
	"github.com/apteva/apps/mcp/computer/internal/browser/keyinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
	"github.com/apteva/apps/mcp/computer/internal/browser/presentation"
	"github.com/apteva/apps/mcp/computer/internal/browser/scrolltarget"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectorclick"
	"github.com/apteva/apps/mcp/computer/internal/browser/som"
	"github.com/apteva/apps/mcp/computer/internal/browser/stability"
	"github.com/apteva/apps/mcp/computer/internal/browser/textinput"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// defaultAPIBase is the Browser Engine API root. Override via
// BROWSER_API_URL env or Options.BaseURL.
const defaultAPIBase = "https://api.browserengine.co"

// Options extends what New accepts beyond apiKey/display. All fields
// are optional. Names match the JSON payload of POST /sessions.
type Options struct {
	// BaseURL overrides the API root (e.g. http://localhost:3000 for
	// local dev). Empty = defaultAPIBase.
	BaseURL string `json:"-"`

	// InitialURL seeds the first navigation server-side before the
	// agent attaches. Saves a round-trip when the start page is known.
	InitialURL string `json:"initial_url,omitempty"`

	// UserAgent overrides the browser's default UA.
	UserAgent string `json:"user_agent,omitempty"`

	// Timeout is the max session duration in seconds. Server picks a
	// default when zero.
	Timeout int `json:"timeout,omitempty"`

	// Proxy configures residential proxying via the upstream service's
	// DataImpulse integration.
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	// ProjectID scopes the session to a Browser Engine project (for
	// quota/billing). Optional.
	ProjectID int `json:"project_id,omitempty"`

	// Metadata is attached to the session record for later querying.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ProxyConfig controls the residential proxy (DataImpulse upstream).
type ProxyConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	Country string `json:"country,omitempty"`
}

type Computer struct {
	apiKey    string
	apiBase   string
	opts      Options
	sessionID string
	contextID string
	debugURL  string
	streamURL string
	// activeProxy is the proxy config we asked the backend to apply
	// for the *current* session — agent's open-time override wins
	// over the harness default. Used by the session-ready log.
	activeProxy *ProxyConfig
	display     computer.DisplaySize
	allocCtx    context.Context
	ctx         context.Context
	cancel      context.CancelFunc
	allocCancel context.CancelFunc
	http        *http.Client
	environment computer.EnvironmentOptions

	// SoM wiring — same as local/browserbase/steel.
	labelMu          sync.RWMutex
	lastLabels       map[int]som.Element
	stabilityTracker *stability.Tracker
	scrollMu         sync.RWMutex
	lastScrollResult *computer.ScrollResult
	scrollRegions    []computer.ScrollRegion

	selectMu         sync.Mutex
	lastSelectResult *selectinput.Result
}

// New constructs a Browser Engine–backed Computer. NO session is
// created yet — the agent picks the binding (anonymous, context, or
// attach to session id) at the first browser_session.open call.
func New(apiKey string, display computer.DisplaySize) (*Computer, error) {
	return NewWithOptions(apiKey, display, Options{})
}

// NewWithOptions stores provider-level configuration for use at
// session-create time. Like New, it does NOT create a session.
func NewWithOptions(apiKey string, display computer.DisplaySize, opts Options) (*Computer, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("browserengine: api_key is required")
	}
	apiBase := opts.BaseURL
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	return &Computer{
		apiKey:  apiKey,
		apiBase: apiBase,
		opts:    opts,
		display: display,
		http:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

type sessionCreateRequest struct {
	// viewport / initial_url / timeout / metadata are always sent —
	// the server rejects the request otherwise.
	Viewport   map[string]int `json:"viewport"`
	InitialURL string         `json:"initial_url"`
	Timeout    int            `json:"timeout"`
	Metadata   map[string]any `json:"metadata"`
	UserAgent  string         `json:"user_agent,omitempty"`
	Proxy      *ProxyConfig   `json:"proxy,omitempty"`
	ProjectID  int            `json:"project_id,omitempty"`
	Context    *contextRef    `json:"context,omitempty"`
}

// contextRef matches the shape `functions/browser/session-create`
// expects: `context: { id, persist }`. The function layer resolves
// id → Docker volume name and forwards to browser-service.
type contextRef struct {
	ID      string `json:"id"`
	Persist bool   `json:"persist"`
}

// sessionResponse captures the fields we read. The server wraps
// the session record inside a top-level {"data": {...}} envelope
// (see browser-app actions.ts), so we decode accordingly.
type sessionResponse struct {
	Data struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		ConnectURL   string `json:"connect_url"`
		CDPJSONURL   string `json:"cdp_json_url"`
		ConnectToken string `json:"connect_token"`
		DebugURL     string `json:"debug_url"`
		StreamURL    string `json:"stream_url"`
	} `json:"data"`
}

func (c *Computer) createSession(o computer.OpenOptions) (string, error) {
	if o.ExternalProxy != nil {
		return "", fmt.Errorf("browser-engine does not support external proxy profiles")
	}
	// Defaults match what frontends/browser/browser-app sends on every
	// session create — the API rejects requests missing initial_url /
	// timeout / metadata even though they're nominally optional.
	initialURL := c.opts.InitialURL
	if o.URL != "" && o.Environment.IsEmpty() {
		// If the agent gave us a target URL, seed it server-side so the
		// session lands ready instead of about:blank.
		initialURL = o.URL
	}
	if initialURL == "" {
		initialURL = "about:blank"
	}
	timeout := c.opts.Timeout
	if o.Timeout > 0 {
		timeout = o.Timeout
	}
	if timeout == 0 {
		// 900s (15 min) is the sane fallback. Browser Engine's prior
		// default was 300s, which expires mid-flow on multi-step
		// agent runs (Patreon login + email-code can take 4–8 min
		// alone). Callers wanting tighter or longer leases set
		// OpenOptions.Timeout explicitly.
		timeout = 900
	}
	metadata := c.opts.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	// Proxy resolution: agent's per-call OpenOptions wins over the
	// harness-time Options default. Nil opts.Proxy = use opts.Proxy
	// from c.opts; explicit &true/&false from the agent overrides.
	proxy := c.opts.Proxy
	if o.Proxy != nil {
		if *o.Proxy {
			proxy = &ProxyConfig{Enabled: true, Country: o.ProxyCountry}
		} else {
			proxy = nil
		}
	}
	c.activeProxy = proxy
	req := sessionCreateRequest{
		Viewport: map[string]int{
			"width":  c.display.Width,
			"height": c.display.Height,
		},
		InitialURL: initialURL,
		UserAgent:  c.opts.UserAgent,
		Timeout:    timeout,
		Proxy:      proxy,
		ProjectID:  c.opts.ProjectID,
		Metadata:   metadata,
	}
	if o.ContextID != "" {
		req.Context = &contextRef{ID: o.ContextID, Persist: o.Persist}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.apiBase+"/sessions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// The envelope varies: some deployments return the session at the
	// top level, others wrap it in {"data": {...}}. Try both.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	var wrapped sessionResponse
	_ = json.Unmarshal(raw, &wrapped)
	got := wrapped.Data
	if got.ID == "" || got.ConnectURL == "" {
		// Fall back to a flat decode.
		var flat struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			ConnectURL   string `json:"connect_url"`
			CDPJSONURL   string `json:"cdp_json_url"`
			ConnectToken string `json:"connect_token"`
			DebugURL     string `json:"debug_url"`
			StreamURL    string `json:"stream_url"`
		}
		if err := json.Unmarshal(raw, &flat); err != nil {
			return "", fmt.Errorf("decode response: %w; body=%s", err, string(raw))
		}
		got = flat
	}

	c.sessionID = got.ID
	c.debugURL = got.DebugURL
	c.streamURL = got.StreamURL

	if got.ConnectURL == "" {
		return "", fmt.Errorf("no connect_url in session response (id=%s status=%s body=%s)",
			got.ID, got.Status, string(raw))
	}

	// The top-level connect_url is a Puppeteer-compatible shortcut
	// (wss://host/sessions/{id}/cdp?token=...). chromedp's raw
	// WebSocket dial against it returns 404 — the browser's actual
	// CDP endpoint lives at /cdp/devtools/browser/{uuid} and is
	// discoverable via /cdp/json/version. Do that two-step so
	// chromedp gets a URL it can upgrade directly, matching the
	// pattern used by services/browser-service's integration test.
	if got.CDPJSONURL != "" && got.ConnectToken != "" {
		ws, err := c.resolveWebSocket(got.CDPJSONURL, got.ConnectToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] /cdp/json/version resolve failed: %v — falling back to connect_url\n", err)
		} else {
			return ws, nil
		}
	}
	return got.ConnectURL, nil
}

// resolveWebSocket fetches /cdp/json/version, extracts
// webSocketDebuggerUrl, and appends the session's connect_token as a
// query param so the upstream accepts the upgrade.
func (c *Computer) resolveWebSocket(jsonURL, token string) (string, error) {
	req, err := http.NewRequest("GET", jsonURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("decode json/version: %w", err)
	}
	if v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("no webSocketDebuggerUrl in /cdp/json/version response")
	}
	sep := "?"
	if strings.Contains(v.WebSocketDebuggerURL, "?") {
		sep = "&"
	}
	return v.WebSocketDebuggerURL + sep + "token=" + token, nil
}

// OpenSession establishes a session matching opts and (if URL set)
// navigates. Three modes:
//
//   - opts.SessionID set         → attach to existing session. Live
//     sessions reattach via GET; terminal
//     ones replay the snapshot via POST
//     .../resume. Backend chooses.
//   - opts.ContextID set         → create new session bound to that
//     persistent context (cookies/storage
//     pre-loaded).
//   - neither                    → fresh anonymous session.
//
// SessionID and ContextID are mutually exclusive. URL is seeded server-
// side at create time (via initial_url) when both URL and ContextID are
// set, avoiding an extra round-trip; otherwise it's a post-attach nav.
func (c *Computer) OpenSession(o computer.OpenOptions) error {
	if o.SessionID != "" && o.ContextID != "" {
		return fmt.Errorf("browserengine: SessionID and ContextID are mutually exclusive")
	}
	if o.SessionID != "" && !o.Environment.IsEmpty() {
		return fmt.Errorf("browserengine: environment settings cannot be applied while attaching to an existing session")
	}
	// Fast path: requested binding is what we already have.
	if c.sessionID != "" {
		sameSession := o.SessionID != "" && o.SessionID == c.sessionID
		sameContext := o.SessionID == "" && o.ContextID != "" && o.ContextID == c.contextID
		if sameSession || sameContext {
			c.environment = o.Environment
			if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
				return fmt.Errorf("browserengine environment: %w", err)
			}
			if o.URL != "" {
				return c.navigate(o.URL)
			}
			return nil
		}
		c.releaseCDP()
		c.sessionID = ""
		c.contextID = ""
		c.debugURL = ""
		c.streamURL = ""
	}

	var connectURL string
	if o.SessionID != "" {
		u, err := c.fetchAttachConnectURL(o.SessionID)
		if err != nil {
			return fmt.Errorf("browserengine: attach %s: %w", o.SessionID, err)
		}
		connectURL = u
		c.sessionID = o.SessionID
	} else {
		u, err := c.createSession(o)
		if err != nil {
			return fmt.Errorf("browserengine: create session: %w", err)
		}
		connectURL = u
		// c.sessionID is set inside createSession.
		if o.ContextID != "" {
			c.contextID = o.ContextID
		}
	}
	if err := c.establishCDP(connectURL, o.SessionID != ""); err != nil {
		return fmt.Errorf("browserengine: connect: %w", err)
	}
	c.environment = o.Environment
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("browserengine environment: %w", err)
	}
	proxyDesc := "off"
	if c.activeProxy != nil && c.activeProxy.Enabled {
		if c.activeProxy.Country != "" {
			proxyDesc = "on country=" + c.activeProxy.Country
		} else {
			proxyDesc = "on"
		}
	}
	fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] session ready id=%s context=%s display=%dx%d proxy=%s\n",
		c.sessionID, c.contextID, c.display.Width, c.display.Height, proxyDesc)
	// Always nav explicitly when a URL is given. We send initial_url at
	// create time too (so the backend can warm the page in parallel
	// with the CDP handshake), but we cannot rely on that nav having
	// completed by the time chromedp.Run returns — empirically Chrome
	// is often still on about:blank. Issuing the nav from our side is
	// idempotent if the backend already landed there.
	if o.URL != "" {
		return c.navigate(o.URL)
	}
	return nil
}

// Resume is a thin wrapper around OpenSession for callers that hold the
// older Resumable interface.
func (c *Computer) Resume(sessionID string) error {
	return c.OpenSession(computer.OpenOptions{SessionID: sessionID})
}

// establishCDP wires up the chromedp remote allocator + a page-level
// context. attach=true means we're reattaching to an already-running
// browser: chromedp.NewContext on its own would create a fresh blank
// tab on top of the existing one, so we first probe the running
// targets and bind to the most relevant pre-existing page (preferring
// non-blank URLs). attach=false skips the probe — the new browser has
// only the create-time tab and chromedp lands on it directly.
func (c *Computer) establishCDP(connectURL string, attach bool) error {
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), connectURL,
		chromedp.NoModifyURL)

	if !attach {
		ctx, cancel := chromedp.NewContext(allocCtx)
		if err := chromedp.Run(ctx); err != nil {
			cancel()
			allocCancel()
			return err
		}
		c.allocCancel = allocCancel
		c.allocCtx = allocCtx
		c.ctx = ctx
		c.cancel = cancel
		c.attachStabilityTracker()
		return nil
	}

	// Attach path. chromedp.Targets needs a chromedp ctx to issue the
	// listing CDP call, so we bootstrap a probe ctx, list, then close
	// it before binding the real ctx to whichever existing target we
	// pick. The probe tab is closed when probeCancel fires.
	probeCtx, probeCancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(probeCtx); err != nil {
		probeCancel()
		allocCancel()
		return fmt.Errorf("attach probe: %w", err)
	}
	infos, err := chromedp.Targets(probeCtx)
	if err != nil {
		probeCancel()
		allocCancel()
		return fmt.Errorf("attach list targets: %w", err)
	}
	probeOwn := ""
	if cdpCtx := chromedp.FromContext(probeCtx); cdpCtx != nil && cdpCtx.Target != nil {
		probeOwn = string(cdpCtx.Target.TargetID)
	}
	pick := pickAttachTarget(infos, probeOwn)
	probeCancel()

	if pick == "" {
		// No existing page worth attaching to — fall back to a fresh tab.
		// (Browser was restarted between session-create and our attach,
		// or the snapshot replay landed on about:blank only.)
		ctx, cancel := chromedp.NewContext(allocCtx)
		if err := chromedp.Run(ctx); err != nil {
			cancel()
			allocCancel()
			return fmt.Errorf("attach fallback ctx: %w", err)
		}
		c.allocCancel = allocCancel
		c.allocCtx = allocCtx
		c.ctx = ctx
		c.cancel = cancel
		c.attachStabilityTracker()
		return nil
	}

	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithTargetID(target.ID(pick)))
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		return fmt.Errorf("attach bind target %s: %w", pick, err)
	}
	c.allocCancel = allocCancel
	c.allocCtx = allocCtx
	c.ctx = ctx
	c.cancel = cancel
	c.attachStabilityTracker()
	fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] attached to existing target %s\n", pick)
	return nil
}

// pickAttachTarget returns the page-type target most worth surfacing
// to the agent. Preference order:
//
//  1. http:// or https:// page (real user content)
//  2. any non-blank page that isn't a chrome://, about:, or devtools://
//     internal URL
//  3. first page-type target (last-resort fallback)
//
// We deliberately deprioritize Chrome internals — a freshly-spawned
// browser tends to have a chrome://new-tab-page sitting alongside the
// page our agent actually navigated to. Without this preference the
// pick is order-of-enumeration roulette.
func pickAttachTarget(infos []*target.Info, probeOwn string) string {
	var firstPage, firstNonInternal, firstWeb string
	for _, t := range infos {
		if t.Type != "page" {
			continue
		}
		if string(t.TargetID) == probeOwn {
			continue
		}
		if firstPage == "" {
			firstPage = string(t.TargetID)
		}
		switch {
		case strings.HasPrefix(t.URL, "http://"), strings.HasPrefix(t.URL, "https://"):
			if firstWeb == "" {
				firstWeb = string(t.TargetID)
			}
		case t.URL != "" && t.URL != "about:blank" &&
			!strings.HasPrefix(t.URL, "chrome://") &&
			!strings.HasPrefix(t.URL, "chrome-extension://") &&
			!strings.HasPrefix(t.URL, "devtools://") &&
			!strings.HasPrefix(t.URL, "view-source:"):
			if firstNonInternal == "" {
				firstNonInternal = string(t.TargetID)
			}
		}
	}
	if firstWeb != "" {
		return firstWeb
	}
	if firstNonInternal != "" {
		return firstNonInternal
	}
	return firstPage
}

func (c *Computer) releaseCDP() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.allocCancel != nil {
		c.allocCancel()
		c.allocCancel = nil
	}
	c.allocCtx = nil
	c.ctx = nil
}

func (c *Computer) ListTabs() ([]computer.TabInfo, error) {
	return cdptabs.List(c.ctx)
}

func (c *Computer) ActiveTabID() string {
	return cdptabs.ActiveID(c.ctx)
}

func (c *Computer) SwitchTab(tabID string) error {
	if tabID == c.ActiveTabID() {
		return nil
	}
	ctx, cancel, err := cdptabs.Switch(c.ctx, tabID)
	if err != nil {
		return err
	}
	c.ctx = ctx
	c.cancel = cancel
	c.attachStabilityTracker()
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("reapply browser environment: %w", err)
	}
	return nil
}

func (c *Computer) CloseTab(tabID string) error {
	tabs, err := c.ListTabs()
	if err != nil {
		return err
	}
	if len(tabs) <= 1 {
		return fmt.Errorf("cannot close last tab; close the browser session instead")
	}
	if tabID == c.ActiveTabID() {
		next := cdptabs.PickFallback(tabs, tabID)
		if next == "" {
			return fmt.Errorf("cannot close active tab without a fallback tab")
		}
		if err := c.SwitchTab(next); err != nil {
			return fmt.Errorf("switch fallback tab: %w", err)
		}
	}
	return cdptabs.Close(c.ctx, tabID)
}

func (c *Computer) navigate(url string) error {
	if c.ctx == nil {
		return fmt.Errorf("browserengine: no active session — cannot navigate")
	}
	_, err := c.Execute(computer.Action{Type: "navigate", URL: url})
	return err
}

// Eval runs a JavaScript expression in the active page and decodes
// the result into dst. Thin wrapper around chromedp.Evaluate. Intended
// for tests + low-level introspection callers — production code should
// drive the page via Execute(). dst follows chromedp.Evaluate semantics
// (JSON-decodable destination, e.g. *string, *map[string]any).
func (c *Computer) Eval(js string, dst any) error {
	if c.ctx == nil {
		return fmt.Errorf("browserengine: no active session — cannot eval")
	}
	return chromedp.Run(c.ctx, chromedp.Evaluate(js, dst))
}

// fetchAttachConnectURL probes GET /sessions/{id} first; if the session
// is RUNNING and exposes connect_url, reuse it. Otherwise fall back to
// POST /sessions/{id}/resume which replays the snapshot.
func (c *Computer) fetchAttachConnectURL(sessionID string) (string, error) {
	if u, ok, err := c.tryGetSessionConnectURL(sessionID); err == nil && ok {
		return u, nil
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] GET /sessions/%s failed (%v) — trying resume\n", sessionID, err)
	}
	return c.postSessionResume(sessionID)
}

func (c *Computer) tryGetSessionConnectURL(sessionID string) (string, bool, error) {
	url := fmt.Sprintf("%s/sessions/%s", c.apiBase, sessionID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var got sessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return "", false, fmt.Errorf("decode: %w", err)
	}
	// Only RUNNING sessions can be reattached without a snapshot replay.
	if got.Data.Status != "RUNNING" || got.Data.ConnectURL == "" {
		return "", false, nil
	}
	if got.Data.CDPJSONURL != "" && got.Data.ConnectToken != "" {
		ws, err := c.resolveWebSocket(got.Data.CDPJSONURL, got.Data.ConnectToken)
		if err == nil {
			return ws, true, nil
		}
	}
	return got.Data.ConnectURL, true, nil
}

func (c *Computer) postSessionResume(sessionID string) (string, error) {
	url := fmt.Sprintf("%s/sessions/%s/resume", c.apiBase, sessionID)
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var got sessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if got.Data.ConnectURL == "" {
		return "", fmt.Errorf("resume returned no connect_url (status=%s)", got.Data.Status)
	}
	if got.Data.CDPJSONURL != "" && got.Data.ConnectToken != "" {
		if ws, err := c.resolveWebSocket(got.Data.CDPJSONURL, got.Data.ConnectToken); err == nil {
			return ws, nil
		}
	}
	return got.Data.ConnectURL, nil
}

// ExtendTimeout sets the session's max lifetime to `seconds` from
// now, via POST /sessions/{id} with {"timeout": N}. The agent calls
// this through browser_session(open, ..., timeout=N) when it expects
// a long-running task (e.g. a login that waits on an emailed code).
// Implements browser.Timeoutable.
func (c *Computer) ExtendTimeout(seconds int) error {
	if c.sessionID == "" {
		return fmt.Errorf("no active session to extend")
	}
	if seconds <= 0 {
		return fmt.Errorf("timeout must be positive, got %d", seconds)
	}
	body, err := json.Marshal(map[string]any{"timeout": seconds})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/sessions/%s", c.apiBase, c.sessionID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer releaseCancel()
	req = req.WithContext(releaseCtx)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] timeout extended id=%s to %ds\n", c.sessionID, seconds)
	return nil
}

// requestRelease ends the session via DELETE /sessions/{id} →
// functions/browser/session-close. We deliberately do NOT use the
// POST .../sessions/{id} (session-update) REQUEST_RELEASE shape:
// session-update sets status=closed and tears down the browser, but
// does NOT release any persistent-context lock the session holds.
// session-close clears `lock_owner_session_id` on the way out, which
// is required for the next OpenSession on the same context to win
// the atomic lock acquisition instead of 409ing.
func (c *Computer) requestRelease() error {
	if c.sessionID == "" {
		return nil
	}
	url := fmt.Sprintf("%s/sessions/%s", c.apiBase, c.sessionID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Computer) Execute(action computer.Action) ([]byte, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("browserengine: no active session — call browser_session open first")
	}
	switch action.Type {
	case "screenshot":
		return c.finishAction(action)

	case "navigate", "back", "reload":
		if err := navigation.Run(c.ctx, action.Type, action.URL, 30*time.Second); err != nil {
			return nil, fmt.Errorf("%s: %w", action.Type, err)
		}
		presentation.AfterAction(action.Presentation, 500*time.Millisecond)
		return c.finishAction(action)

	case "click":
		x, y := action.X, action.Y
		expectedText := action.ExpectedText
		if action.Selector != "" {
			point, err := selectorclick.Resolve(c.ctx, action.Selector)
			if err != nil {
				return nil, fmt.Errorf("click: %w", err)
			}
			x, y = point.X, point.Y
		} else if action.Label != 0 {
			if e, ok := c.resolveLabel(action.Label); ok {
				x, y = e.Center()
				if expectedText == "" {
					expectedText = e.AccessibleName
					if expectedText == "" {
						expectedText = e.Text
					}
				}
			}
		}
		if err := presentation.BeforeClick(c.ctx, x, y, action.Presentation); err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] presentation cursor unavailable, continuing click: %v\n", err)
		}
		guardOptions := clickguard.Options{
			TargetID:     action.TargetID,
			ExpectedText: expectedText, ExpectedEffect: action.ExpectedEffect, ConfirmConsequence: action.ConfirmConsequence,
			EnforceConsequence: action.EnforceConsequence, RequireExpectedIfDangerous: action.GuardDangerousCoordinate,
		}
		target, clickErr := clickguard.Click(c.ctx, x, y, 1, guardOptions)
		clickguard.StoreResult(action.ClickResult, target, guardOptions, clickErr == nil)
		if clickErr != nil {
			return nil, fmt.Errorf("click: %w", clickErr)
		}
		focusJS := fmt.Sprintf(`(function(){
			var el = document.elementFromPoint(%d, %d);
			if (el && typeof el.focus === 'function') { el.focus(); return el.tagName; }
			return null;
		})()`, x, y)
		var focusedTag string
		_ = chromedp.Run(c.ctx, chromedp.Evaluate(focusJS, &focusedTag))
		if focusedTag != "" {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] click focused <%s>\n", strings.ToLower(focusedTag))
		}
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.finishAction(action)

	case "double_click":
		x, y := action.X, action.Y
		expectedText := action.ExpectedText
		if action.Label != 0 {
			if e, ok := c.resolveLabel(action.Label); ok {
				x, y = e.Center()
				if expectedText == "" {
					expectedText = e.AccessibleName
					if expectedText == "" {
						expectedText = e.Text
					}
				}
			}
		}
		if err := presentation.BeforeClick(c.ctx, x, y, action.Presentation); err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] presentation cursor unavailable, continuing double click: %v\n", err)
		}
		guardOptions := clickguard.Options{
			TargetID:     action.TargetID,
			ExpectedText: expectedText, ExpectedEffect: action.ExpectedEffect, ConfirmConsequence: action.ConfirmConsequence,
			EnforceConsequence: action.EnforceConsequence, RequireExpectedIfDangerous: action.GuardDangerousCoordinate,
		}
		target, clickErr := clickguard.Click(c.ctx, x, y, 2, guardOptions)
		clickguard.StoreResult(action.ClickResult, target, guardOptions, clickErr == nil)
		if clickErr != nil {
			return nil, fmt.Errorf("double_click: %w", clickErr)
		}
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.finishAction(action)

	case "type":
		delay := time.Duration(action.Presentation.TypingDelayMS) * time.Millisecond
		if err := textinput.TypeWithDelay(c.ctx, action.Text, "[BROWSER_ENGINE]", delay); err != nil {
			return nil, fmt.Errorf("type: %w", err)
		}
		presentation.AfterAction(action.Presentation, 100*time.Millisecond)
		return c.Screenshot()

	case "key":
		if err := keyinput.Dispatch(c.ctx, action.Key, "[BROWSER_ENGINE]"); err != nil {
			return nil, fmt.Errorf("key: %w", err)
		}
		presentation.AfterAction(action.Presentation, 100*time.Millisecond)
		return c.Screenshot()

	case "scroll":
		if err := c.scroll(action); err != nil {
			return nil, fmt.Errorf("scroll: %w", err)
		}
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.finishAction(action)

	case "wait":
		dur := action.Duration
		if dur <= 0 {
			dur = 1000
		}
		time.Sleep(time.Duration(dur) * time.Millisecond)
		return c.finishAction(action)

	case "wait_for_stable":
		if _, err := c.WaitForStable(action.QuietMS, action.TimeoutMS); err != nil {
			return nil, fmt.Errorf("wait_for_stable: %w", err)
		}
		return c.finishAction(action)

	case "wait_for":
		if _, err := c.WaitForOutcome(action.Conditions, action.Match, action.QuietMS, action.TimeoutMS); err != nil {
			return nil, fmt.Errorf("wait_for: %w", err)
		}
		return c.finishAction(action)

	case "select_option":
		c.moveToTarget(action)
		res, err := c.selectOption(action)
		if err != nil {
			return nil, fmt.Errorf("select_option: %w", err)
		}
		c.cueTarget(action, res.Selector, "Option selected")
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.Screenshot()

	default:
		return nil, fmt.Errorf("unknown action: %s", action.Type)
	}
}

func (c *Computer) finishAction(action computer.Action) ([]byte, error) {
	if action.NoScreenshot {
		return nil, nil
	}
	return c.Screenshot()
}

func (c *Computer) ExecuteAction(action computer.Action) error {
	switch action.Type {
	case "click", "double_click", "scroll", "wait", "wait_for", "wait_for_stable":
		action.NoScreenshot = true
		_, err := c.Execute(action)
		return err
	default:
		return fmt.Errorf("browser-engine action-only unsupported for %s", action.Type)
	}
}

func (c *Computer) attachStabilityTracker() {
	c.stabilityTracker = nil
	if c.ctx == nil {
		return
	}
	tracker, err := stability.New(c.ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSER-ENGINE] stability tracker unavailable: %v\n", err)
		return
	}
	c.stabilityTracker = tracker
}

func (c *Computer) WaitForStable(quietMS, timeoutMS int) (stability.Result, error) {
	if c.stabilityTracker != nil {
		return c.stabilityTracker.Wait(quietMS, timeoutMS)
	}
	return stability.Wait(c.ctx, quietMS, timeoutMS)
}

func (c *Computer) WaitForOutcome(conditions []computer.WaitCondition, match string, quietMS, timeoutMS int) (stability.Result, error) {
	if c.stabilityTracker != nil {
		return c.stabilityTracker.WaitForOutcome(conditions, match, quietMS, timeoutMS)
	}
	return stability.WaitForOutcome(c.ctx, conditions, match, quietMS, timeoutMS)
}

func (c *Computer) ObserveMedia() (computer.MediaObservation, error) {
	return stability.ObserveMedia(c.ctx)
}

func (c *Computer) selectOption(action computer.Action) (selectinput.Result, error) {
	c.setLastSelectResult(nil)
	target := selectinput.Target{Selector: action.Selector}
	if action.Label > 0 {
		if e, ok := c.resolveLabel(action.Label); ok {
			target.X, target.Y = e.Center()
			target.HasPoint = true
		}
	}
	if !target.HasPoint && action.X != 0 && action.Y != 0 {
		target.X, target.Y = action.X, action.Y
		target.HasPoint = true
	}
	res, err := selectinput.Select(c.ctx, target, selectinput.Request{
		Text:   action.Text,
		Value:  action.Value,
		Texts:  action.Texts,
		Values: action.Values,
		Mode:   action.Mode,
	})
	if err == nil || res.Selector != "" || res.ErrorCode != "" {
		c.setLastSelectResult(&res)
	}
	return res, err
}

func (c *Computer) cueTarget(action computer.Action, selector, caption string) {
	if !action.Presentation.Enabled() {
		return
	}
	x, y := action.X, action.Y
	hasPoint := x != 0 && y != 0
	if action.Label > 0 {
		if e, ok := c.resolveLabel(action.Label); ok {
			x, y = e.Center()
			hasPoint = true
		}
	}
	if err := presentation.CueTarget(
		c.ctx,
		selector,
		x,
		y,
		hasPoint,
		caption,
		action.Presentation,
	); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] presentation cue unavailable, continuing action: %v\n", err)
	}
}

func (c *Computer) moveToTarget(action computer.Action) {
	if !action.Presentation.Enabled() {
		return
	}
	x, y := action.X, action.Y
	hasPoint := x != 0 && y != 0
	if action.Label > 0 {
		if e, ok := c.resolveLabel(action.Label); ok {
			x, y = e.Center()
			hasPoint = true
		}
	}
	if err := presentation.MoveToTarget(
		c.ctx,
		action.Selector,
		x,
		y,
		hasPoint,
		action.Presentation,
	); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] presentation move unavailable, continuing action: %v\n", err)
	}
}

func (c *Computer) LastSelectResult() *selectinput.Result {
	c.selectMu.Lock()
	defer c.selectMu.Unlock()
	return cloneSelectResult(c.lastSelectResult)
}

func (c *Computer) setLastSelectResult(res *selectinput.Result) {
	c.selectMu.Lock()
	defer c.selectMu.Unlock()
	c.lastSelectResult = cloneSelectResult(res)
}

func cloneSelectResult(res *selectinput.Result) *selectinput.Result {
	if res == nil {
		return nil
	}
	clone := *res
	clone.RequestedOptions = append([]string(nil), res.RequestedOptions...)
	clone.Matched = append([]string(nil), res.Matched...)
	clone.Selected = append([]string(nil), res.Selected...)
	clone.Options = append([]selectinput.Option(nil), res.Options...)
	return &clone
}

func (c *Computer) scroll(a computer.Action) error {
	if a.Label > 0 && a.TargetID == "" {
		if e, ok := c.resolveLabel(a.Label); ok {
			a.X, a.Y = e.Center()
		}
	}
	result, err := scrolltarget.Run(c.ctx, a, c.display)
	c.scrollMu.Lock()
	c.lastScrollResult = scrolltarget.CloneResult(&result)
	if err == nil {
		c.scrollRegions = scrolltarget.CloneRegions(result.Regions)
	}
	c.scrollMu.Unlock()
	return err
}

func (c *Computer) LastScrollResult() *computer.ScrollResult {
	c.scrollMu.RLock()
	defer c.scrollMu.RUnlock()
	return scrolltarget.CloneResult(c.lastScrollResult)
}
func (c *Computer) ScrollRegions() []computer.ScrollRegion {
	c.scrollMu.RLock()
	defer c.scrollMu.RUnlock()
	return scrolltarget.CloneRegions(c.scrollRegions)
}
func (c *Computer) refreshScrollRegions() {
	regions, err := scrolltarget.Enumerate(c.ctx)
	if err != nil {
		return
	}
	c.scrollMu.Lock()
	c.scrollRegions = scrolltarget.CloneRegions(regions)
	c.scrollMu.Unlock()
}

func (c *Computer) Screenshot() ([]byte, error) {
	return c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true})
}

func (c *Computer) ScreenshotWithOptions(options computer.ScreenshotOptions) ([]byte, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("browserengine: no active session — call browser_session open first")
	}
	if options.Annotate {
		c.labelMu.Lock()
		c.lastLabels = nil
		c.labelMu.Unlock()
	}
	var buf []byte
	err := chromedp.Run(c.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			b, err := page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatJpeg).
				WithQuality(90).
				Do(ctx)
			if err != nil {
				return err
			}
			buf = b
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}

	if options.Annotate && som.Enabled() {
		var raw json.RawMessage
		if err := chromedp.Run(c.ctx, chromedp.Evaluate(som.EnumScript, &raw)); err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] som enum failed: %v\n", err)
			return buf, nil
		}
		elements, err := som.UnmarshalElements(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] som parse failed: %v\n", err)
			return buf, nil
		}
		if som.ShouldAugmentAX(elements) {
			elements = som.MergeAX(elements, som.EnumerateViaAX(c.ctx, c.display.Width, c.display.Height))
		}
		m := make(map[int]som.Element, len(elements))
		for _, e := range elements {
			m[e.Label] = e
		}
		c.labelMu.Lock()
		c.lastLabels = m
		c.labelMu.Unlock()
		c.refreshScrollRegions()

		annotated, aerr := som.Annotate(buf, elements)
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] som annotate failed: %v\n", aerr)
			return buf, nil
		}
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] som annotated: %d elements\n", len(elements))
		return annotated, nil
	}
	return buf, nil
}

func (c *Computer) resolveLabel(label int) (som.Element, bool) {
	c.labelMu.RLock()
	defer c.labelMu.RUnlock()
	if c.lastLabels == nil {
		return som.Element{}, false
	}
	e, ok := c.lastLabels[label]
	return e, ok
}

func (c *Computer) LastSetOfMark() []computer.SetOfMarkTarget {
	c.labelMu.RLock()
	defer c.labelMu.RUnlock()
	if len(c.lastLabels) == 0 {
		return nil
	}
	labels := make([]int, 0, len(c.lastLabels))
	for label := range c.lastLabels {
		labels = append(labels, label)
	}
	sort.Ints(labels)
	out := make([]computer.SetOfMarkTarget, 0, len(labels))
	for _, label := range labels {
		e := c.lastLabels[label]
		out = append(out, computer.SetOfMarkTarget{
			ID: e.ID, Label: e.Label, X: e.X, Y: e.Y, W: e.W, H: e.H,
			Tag: e.Tag, Role: e.Role, Text: e.Text, AccessibleName: e.AccessibleName, Type: e.Type,
			Placeholder: e.Placeholder, CurrentValue: e.CurrentValue, Pattern: e.Pattern,
			FormatHint: e.FormatHint, DateLike: e.DateLike, Validity: e.Validity,
			Disabled: e.Disabled, Loading: e.Loading, TargetLoading: e.TargetLoading,
			ContainerLoading: e.ContainerLoading, PageLoadingCount: e.PageLoadingCount,
			Dangerous: e.Dangerous, Effect: e.Effect, DestructiveEffect: e.DestructiveEffect,
		})
	}
	return out
}

func (c *Computer) DisplaySize() computer.DisplaySize { return c.display }

func (c *Computer) EffectiveEnvironment() (computer.EffectiveEnvironment, error) {
	return environment.Probe(c.ctx)
}

func (c *Computer) SessionType() string { return "browser-engine" }
func (c *Computer) SessionID() string   { return c.sessionID }
func (c *Computer) ContextID() string   { return c.contextID }
func (c *Computer) CurrentURL() string {
	var url string
	_ = chromedp.Run(c.ctx, chromedp.Location(&url))
	return url
}

func (c *Computer) ExtractDOM(opts computer.ExtractOptions) (computer.ExtractResult, error) {
	if c.ctx == nil {
		return computer.ExtractResult{}, fmt.Errorf("browser-engine: no active session — call browser_session open first")
	}
	return domextract.Run(c.ctx, opts)
}

// DebugURL returns the Browser Engine debugger page URL, or "" if
// unavailable.
func (c *Computer) DebugURL() string { return c.debugURL }

// StreamURL returns the live browser view stream URL, or "" if
// unavailable. Type-assert to access from a caller that wants a
// "watch live" link without widening the core Computer interface.
func (c *Computer) StreamURL() string { return c.streamURL }

func (c *Computer) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.allocCancel != nil {
		c.allocCancel()
	}
	c.allocCtx = nil
	c.ctx = nil
	c.cancel = nil
	c.allocCancel = nil

	if err := c.requestRelease(); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] release failed id=%s: %v\n", c.sessionID, err)
	} else if c.sessionID != "" {
		fmt.Fprintf(os.Stderr, "[BROWSER_ENGINE] session released id=%s\n", c.sessionID)
	}
	c.sessionID = ""
	return nil
}
