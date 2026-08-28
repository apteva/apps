// Package steel implements the Computer interface using Steel.dev sessions via chromedp/CDP.
package steel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	"github.com/chromedp/chromedp"
)

// apiBase is the Steel REST API root. Tests override this.
var apiBase = "https://api.steel.dev/v1"

// Options extends what New accepts beyond apiKey/display. All fields are
// optional. Field names match Steel's POST /v1/sessions payload
// (https://docs.steel.dev/api-reference) so omitempty leaves Steel's
// server-side defaults in place.
type Options struct {
	// BlockAds enables Steel's built-in ad blocker.
	BlockAds bool `json:"blockAds,omitempty"`

	// ProxyURL pins the session to a specific upstream proxy. Mutually
	// exclusive with UseProxy (managed residential proxy).
	ProxyURL string `json:"proxyUrl,omitempty"`

	// UseProxy enables Steel's managed residential proxy.
	UseProxy bool `json:"useProxy,omitempty"`

	// Region pins the session to a Steel region (e.g. "lax1", "iad1").
	// Default: Steel picks nearest.
	Region string `json:"region,omitempty"`

	// Timeout is the max session duration in milliseconds. Default and
	// max depend on plan.
	Timeout int `json:"timeout,omitempty"`

	// SolveCaptcha enables Steel's managed CAPTCHA solver.
	SolveCaptcha bool `json:"solveCaptcha,omitempty"`

	// UserAgent overrides the browser's default user agent.
	UserAgent string `json:"userAgent,omitempty"`

	// SessionContext lets the caller seed cookies/localStorage as an
	// opaque inline blob (one-shot, not persisted across sessions).
	// Forwarded as-is. Use OpenOptions.ContextID for the persistent
	// cross-session identity that Steel calls a "profile" — see
	// OpenSession below. This field is here only as an escape hatch
	// for callers that already have a snapshot blob from Steel's
	// contexts endpoint.
	SessionContext map[string]any `json:"sessionContext,omitempty"`
}

type Computer struct {
	apiKey      string
	opts        Options
	sessionID   string
	contextID   string
	viewerURL   string
	display     computer.DisplaySize
	allocCtx    context.Context
	ctx         context.Context
	cancel      context.CancelFunc
	allocCancel context.CancelFunc
	http        *http.Client
	environment computer.EnvironmentOptions

	// SoM: same wiring as local.Computer / browserbase.Computer.
	labelMu          sync.RWMutex
	lastLabels       map[int]som.Element
	stabilityTracker *stability.Tracker
	scrollMu         sync.RWMutex
	lastScrollResult *computer.ScrollResult
	scrollRegions    []computer.ScrollRegion

	selectMu         sync.Mutex
	lastSelectResult *selectinput.Result
}

// New constructs a Steel-backed Computer. NO session is created yet —
// the agent picks the binding (anonymous or context/profile) at the
// first browser_session.open call. Steel does not support attaching
// to an existing session id.
func New(apiKey string, display computer.DisplaySize) (*Computer, error) {
	return NewWithOptions(apiKey, display, Options{})
}

// NewWithOptions stores provider-level configuration for use at
// session-create time. Like New, it does NOT create a session.
func NewWithOptions(apiKey string, display computer.DisplaySize, opts Options) (*Computer, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("steel: api_key is required")
	}
	return &Computer{
		apiKey:  apiKey,
		opts:    opts,
		display: display,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// sessionCreateRequest is the POST /v1/sessions payload. camelCase to
// match Steel's schema.
type sessionCreateRequest struct {
	Dimensions     map[string]int `json:"dimensions,omitempty"`
	BlockAds       bool           `json:"blockAds,omitempty"`
	ProxyURL       string         `json:"proxyUrl,omitempty"`
	UseProxy       bool           `json:"useProxy,omitempty"`
	Region         string         `json:"region,omitempty"`
	Timeout        int            `json:"timeout,omitempty"`
	SolveCaptcha   bool           `json:"solveCaptcha,omitempty"`
	UserAgent      string         `json:"userAgent,omitempty"`
	SessionContext map[string]any `json:"sessionContext,omitempty"`
	ProfileID      string         `json:"profileId,omitempty"`
	PersistProfile bool           `json:"persistProfile,omitempty"`
}

type sessionCreateResponse struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	WebsocketURL     string `json:"websocketUrl"`
	SessionViewerURL string `json:"sessionViewerUrl"`
	DebugURL         string `json:"debugUrl"`
	ProfileID        string `json:"profileId"`
}

func (c *Computer) createSession(o computer.OpenOptions) (string, error) {
	timeout := c.opts.Timeout
	if o.Timeout > 0 {
		// Steel's API takes ms; OpenOptions.Timeout is seconds.
		timeout = o.Timeout * 1000
	}
	// Agent's per-call OpenOptions.Proxy wins over the harness default.
	// ProxyCountry is not honored — Steel's useProxy boolean doesn't
	// take a country (custom routing requires the ProxyURL escape
	// hatch in Options).
	useProxy := c.opts.UseProxy
	proxyURL := c.opts.ProxyURL
	if o.Proxy != nil {
		useProxy = *o.Proxy
	}
	if o.ExternalProxy != nil {
		resolved, err := authenticatedProxyURL(o.ExternalProxy)
		if err != nil {
			return "", err
		}
		proxyURL = resolved
		useProxy = false
	}
	req := sessionCreateRequest{
		Dimensions: map[string]int{
			"width":  c.display.Width,
			"height": c.display.Height,
		},
		BlockAds:       c.opts.BlockAds,
		ProxyURL:       proxyURL,
		UseProxy:       useProxy,
		Region:         c.opts.Region,
		Timeout:        timeout,
		SolveCaptcha:   c.opts.SolveCaptcha,
		UserAgent:      c.opts.UserAgent,
		SessionContext: c.opts.SessionContext,
	}
	if o.ContextID != "" {
		req.ProfileID = o.ContextID
		req.PersistProfile = o.Persist
	} else if o.CreateContext {
		req.PersistProfile = true
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", apiBase+"/sessions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Steel-Api-Key", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result sessionCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	c.sessionID = result.ID
	if result.ProfileID != "" {
		c.contextID = result.ProfileID
	}
	if result.SessionViewerURL != "" {
		c.viewerURL = result.SessionViewerURL
	} else {
		c.viewerURL = result.DebugURL
	}

	if result.WebsocketURL == "" {
		return "", fmt.Errorf("no websocketUrl in session response (id=%s status=%s)", result.ID, result.Status)
	}
	// Steel's CDP endpoint requires the API key as a query parameter.
	sep := "?"
	if strings.Contains(result.WebsocketURL, "?") {
		sep = "&"
	}
	return result.WebsocketURL + sep + "apiKey=" + c.apiKey, nil
}

func authenticatedProxyURL(proxy *computer.ExternalProxy) (string, error) {
	if proxy == nil || strings.TrimSpace(proxy.Server) == "" {
		return "", fmt.Errorf("steel: external proxy server is required")
	}
	u, err := url.Parse(strings.TrimSpace(proxy.Server))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("steel: invalid external proxy server")
	}
	if proxy.Username != "" {
		u.User = url.UserPassword(proxy.Username, proxy.Password)
	}
	return u.String(), nil
}

// requestRelease ends the session via POST /v1/sessions/{id}/release.
func (c *Computer) requestRelease() error {
	if c.sessionID == "" {
		return nil
	}
	url := fmt.Sprintf("%s/sessions/%s/release", apiBase, c.sessionID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Steel-Api-Key", c.apiKey)
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
	return nil
}

// OpenSession establishes a session matching opts and (if URL set) navigates.
// Steel maps ContextID/Persist → profileId/persistProfile. SessionID-based
// attach is unsupported (Steel offers no session reconnection — each
// resume is a fresh session bound to the same profile).
func (c *Computer) OpenSession(o computer.OpenOptions) error {
	if o.SessionID != "" {
		return fmt.Errorf("steel: SessionID-based attach is not supported (open with the same context_id to reuse the profile)")
	}
	// Fast path: same context already attached, just navigate.
	if c.sessionID != "" && o.ContextID != "" && o.ContextID == c.contextID {
		c.environment = o.Environment
		if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
			return fmt.Errorf("steel environment: %w", err)
		}
		if o.URL != "" {
			return c.navigate(o.URL)
		}
		return nil
	}
	if c.sessionID != "" {
		c.releaseCDP()
		c.sessionID = ""
		c.contextID = ""
		c.viewerURL = ""
	}
	connectURL, err := c.createSession(o)
	if err != nil {
		return fmt.Errorf("steel: create session: %w", err)
	}
	if o.ContextID != "" {
		c.contextID = o.ContextID
	}
	if err := c.establishCDP(connectURL); err != nil {
		return fmt.Errorf("steel: connect: %w", err)
	}
	c.environment = o.Environment
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("steel environment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[STEEL] session ready id=%s context=%s viewer=%s display=%dx%d\n",
		c.sessionID, c.contextID, c.viewerURL, c.display.Width, c.display.Height)
	if o.URL != "" {
		return c.navigate(o.URL)
	}
	return nil
}

func (c *Computer) establishCDP(connectURL string) error {
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), connectURL,
		chromedp.NoModifyURL)
	ctx, cancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		return err
	}
	c.allocCancel = allocCancel
	c.allocCtx = allocCtx
	c.ctx = ctx
	c.attachStabilityTracker()
	c.cancel = cancel
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("reapply browser environment: %w", err)
	}
	return nil
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
		return fmt.Errorf("steel: no active session — cannot navigate")
	}
	_, err := c.Execute(computer.Action{Type: "navigate", URL: url})
	return err
}

func (c *Computer) Execute(action computer.Action) ([]byte, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("steel: no active session — call browser_session open first")
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
			fmt.Fprintf(os.Stderr, "[STEEL] presentation cursor unavailable, continuing click: %v\n", err)
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
		// Explicit focus at the click point — same rationale as the
		// local and browserbase packages.
		focusJS := fmt.Sprintf(`(function(){
			var el = document.elementFromPoint(%d, %d);
			if (el && typeof el.focus === 'function') { el.focus(); return el.tagName; }
			return null;
		})()`, x, y)
		var focusedTag string
		_ = chromedp.Run(c.ctx, chromedp.Evaluate(focusJS, &focusedTag))
		if focusedTag != "" {
			fmt.Fprintf(os.Stderr, "[STEEL] click focused <%s>\n", strings.ToLower(focusedTag))
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
			fmt.Fprintf(os.Stderr, "[STEEL] presentation cursor unavailable, continuing double click: %v\n", err)
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
		if err := textinput.TypeWithDelay(c.ctx, action.Text, "[STEEL]", delay); err != nil {
			return nil, fmt.Errorf("type: %w", err)
		}
		presentation.AfterAction(action.Presentation, 100*time.Millisecond)
		return c.Screenshot()

	case "key":
		if err := keyinput.Dispatch(c.ctx, action.Key, "[STEEL]"); err != nil {
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
		return fmt.Errorf("steel action-only unsupported for %s", action.Type)
	}
}

func (c *Computer) attachStabilityTracker() {
	c.stabilityTracker = nil
	if c.ctx == nil {
		return
	}
	tracker, err := stability.New(c.ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[STEEL] stability tracker unavailable: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "[STEEL] presentation cue unavailable, continuing action: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "[STEEL] presentation move unavailable, continuing action: %v\n", err)
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
		return nil, fmt.Errorf("steel: no active session — call browser_session open first")
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
			fmt.Fprintf(os.Stderr, "[STEEL] som enum failed: %v\n", err)
			return buf, nil
		}
		elements, err := som.UnmarshalElements(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[STEEL] som parse failed: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "[STEEL] som annotate failed: %v\n", aerr)
			return buf, nil
		}
		fmt.Fprintf(os.Stderr, "[STEEL] som annotated: %d elements\n", len(elements))
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

func (c *Computer) SessionType() string { return "steel" }
func (c *Computer) SessionID() string   { return c.sessionID }
func (c *Computer) ContextID() string   { return c.contextID }
func (c *Computer) CurrentURL() string {
	var url string
	_ = chromedp.Run(c.ctx, chromedp.Location(&url))
	return url
}

func (c *Computer) ExtractDOM(opts computer.ExtractOptions) (computer.ExtractResult, error) {
	if c.ctx == nil {
		return computer.ExtractResult{}, fmt.Errorf("steel: no active session — call browser_session open first")
	}
	return domextract.Run(c.ctx, opts)
}

// DebugURL returns the Steel session viewer URL, or "" if unavailable.
func (c *Computer) DebugURL() string { return c.viewerURL }

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
		fmt.Fprintf(os.Stderr, "[STEEL] release failed id=%s: %v\n", c.sessionID, err)
	} else if c.sessionID != "" {
		fmt.Fprintf(os.Stderr, "[STEEL] session released id=%s\n", c.sessionID)
	}
	c.sessionID = ""
	return nil
}
