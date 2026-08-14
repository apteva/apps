// Package browserbase implements the Computer interface using Browserbase sessions via chromedp/CDP.
package browserbase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdptabs"
	"github.com/apteva/apps/mcp/computer/internal/browser/checkedinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/clickguard"
	"github.com/apteva/apps/mcp/computer/internal/browser/domextract"
	"github.com/apteva/apps/mcp/computer/internal/browser/environment"
	"github.com/apteva/apps/mcp/computer/internal/browser/fileupload"
	"github.com/apteva/apps/mcp/computer/internal/browser/keyinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
	"github.com/apteva/apps/mcp/computer/internal/browser/presentation"
	"github.com/apteva/apps/mcp/computer/internal/browser/scrolltarget"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectorclick"
	"github.com/apteva/apps/mcp/computer/internal/browser/som"
	"github.com/apteva/apps/mcp/computer/internal/browser/stability"
	"github.com/apteva/apps/mcp/computer/internal/browser/temporalinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/textinput"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// apiBase is the Browserbase REST API root. Tests override this.
var apiBase = "https://api.browserbase.com/v1"

const (
	screenshotCaptureAttempts   = 2
	screenshotCaptureTimeout    = 5 * time.Second
	screenshotCaptureRetryDelay = 750 * time.Millisecond
	clickActionTimeout          = 20 * time.Second
	textActionTimeout           = 15 * time.Second
	keyActionTimeout            = 15 * time.Second
	scrollActionTimeout         = 15 * time.Second
	waitActionTimeout           = 35 * time.Second
	navigateActionTimeout       = 30 * time.Second
	inlineUploadMaxBytes        = 64 * 1024 * 1024
)

const screenshotRecoveryFreshTarget = "fresh_target_same_url"

// Options extends what New accepts beyond apiKey/projectID/display. All
// fields are optional. They correspond 1:1 to the POST /v1/sessions payload
// documented at https://docs.browserbase.com/reference/api/create-a-session.
type Options struct {
	// KeepAlive keeps the session alive after the CDP client disconnects
	// (paid plans only). Default false.
	KeepAlive bool `json:"keepAlive,omitempty"`

	// Region pins the session to a Browserbase region:
	// "us-west-2", "us-east-1", "eu-central-1", "ap-southeast-1".
	// Default: Browserbase picks nearest.
	Region string `json:"region,omitempty"`

	// Timeout is the max session duration in seconds. Default and max
	// depend on plan (Free: 3600).
	Timeout int `json:"timeout,omitempty"`

	// Proxies: true enables Browserbase's managed residential proxy, or
	// pass a raw list for custom proxies. Encoded as-is into the request.
	Proxies any `json:"proxies,omitempty"`

	// Fingerprint settings (device, locales, OS, screen). Opaque — passed through.
	Fingerprint map[string]any `json:"fingerprint,omitempty"`

	// ExtensionID attaches a previously uploaded Chrome extension to the session.
	ExtensionID string `json:"extensionId,omitempty"`

	// SolveCaptchas enables Browserbase's managed CAPTCHA solver.
	SolveCaptchas bool `json:"solveCaptchas,omitempty"`

	// UserMetadata is attached to the session record for later querying.
	UserMetadata map[string]any `json:"userMetadata,omitempty"`
}

type Computer struct {
	apiKey          string
	projectID       string
	opts            Options
	sessionID       string
	contextID       string
	contextPersist  bool
	debugURL        string
	display         computer.DisplaySize
	allocCtx        context.Context
	ctx             context.Context
	cancel          context.CancelFunc
	allocCancel     context.CancelFunc
	http            *http.Client
	environment     computer.EnvironmentOptions
	usageMu         sync.Mutex
	closedSessionID string
	lastUsage       *computer.SessionUsage

	// SoM: same wiring as local.Computer. See local.go for rationale.
	labelMu          sync.RWMutex
	lastLabels       map[int]som.Element
	stabilityTracker *stability.Tracker
	scrollMu         sync.RWMutex
	lastScrollResult *computer.ScrollResult
	scrollRegions    []computer.ScrollRegion

	selectMu           sync.Mutex
	lastSelectResult   *selectinput.Result
	checkedMu          sync.Mutex
	lastCheckedResult  *checkedinput.Result
	temporalMu         sync.Mutex
	lastTemporalResult *temporalinput.Result
	textMu             sync.Mutex
	lastTextResult     *textinput.SetResult

	recoveryMu            sync.Mutex
	lastScreenshotRecover *computer.ScreenshotRecoveryInfo
}

// New constructs a Browserbase-backed Computer. NO session is created
// yet — the agent picks the binding (anonymous, context, or attach to
// session id) via the first browser_session.open call, which routes
// through OpenSession.
func New(apiKey, projectID string, display computer.DisplaySize) (*Computer, error) {
	return NewWithOptions(apiKey, projectID, display, Options{})
}

// NewWithOptions stores provider-level configuration (Region, Timeout,
// KeepAlive, Fingerprint, …) for use at session-create time. Like New,
// it does NOT create a session — that's deferred to OpenSession.
func NewWithOptions(apiKey, projectID string, display computer.DisplaySize, opts Options) (*Computer, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("browserbase: api_key is required")
	}
	if projectID == "" {
		// The API requires projectId. Fail fast with a clear message
		// instead of surfacing a confusing HTTP 400 from the server.
		return nil, fmt.Errorf("browserbase: project_id is required")
	}
	return &Computer{
		apiKey:    apiKey,
		projectID: projectID,
		opts:      opts,
		display:   display,
		http:      &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// sessionCreateRequest is the POST /v1/sessions payload. Fields match
// Browserbase's documented schema; omitempty means unset values don't
// override Browserbase's server-side defaults.
type sessionCreateRequest struct {
	ProjectID       string         `json:"projectId"`
	BrowserSettings map[string]any `json:"browserSettings,omitempty"`
	KeepAlive       bool           `json:"keepAlive,omitempty"`
	Region          string         `json:"region,omitempty"`
	Timeout         int            `json:"timeout,omitempty"`
	Proxies         any            `json:"proxies,omitempty"`
	ExtensionID     string         `json:"extensionId,omitempty"`
	UserMetadata    map[string]any `json:"userMetadata,omitempty"`
}

type sessionCreateResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ConnectURL string `json:"connectUrl"`
	// The API also returns projectId, createdAt, region, etc.; we only
	// read the fields we use.
}

func resolveProxies(defaults any, o computer.OpenOptions) (any, error) {
	if o.ExternalProxy != nil {
		server := strings.TrimSpace(o.ExternalProxy.Server)
		if server == "" {
			return nil, fmt.Errorf("external proxy server is required")
		}
		if !strings.HasPrefix(strings.ToLower(server), "http://") && !strings.HasPrefix(strings.ToLower(server), "https://") {
			return nil, fmt.Errorf("Browserbase external proxies require an HTTP or HTTPS server")
		}
		proxy := map[string]any{"type": "external", "server": server}
		if o.ExternalProxy.Username != "" {
			proxy["username"] = o.ExternalProxy.Username
		}
		if o.ExternalProxy.Password != "" {
			proxy["password"] = o.ExternalProxy.Password
		}
		return []map[string]any{proxy}, nil
	}
	country := strings.ToUpper(strings.TrimSpace(o.ProxyCountry))
	if country != "" {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return nil, fmt.Errorf("proxy_country must be a two-letter ISO country code")
		}
		if o.Proxy != nil && !*o.Proxy {
			return nil, fmt.Errorf("proxy_country cannot be used with proxy=false")
		}
		return []map[string]any{
			{
				"type": "browserbase",
				"geolocation": map[string]string{
					"country": country,
				},
			},
		}, nil
	}

	// The agent's per-call choice wins over the provider-level default.
	if o.Proxy != nil {
		if *o.Proxy {
			return true, nil
		}
		return nil, nil
	}
	return defaults, nil
}

func (c *Computer) createSession(o computer.OpenOptions) (string, error) {
	bs := map[string]any{
		"viewport": map[string]int{
			"width":  c.display.Width,
			"height": c.display.Height,
		},
	}
	if c.opts.Fingerprint != nil {
		bs["fingerprint"] = c.opts.Fingerprint
	}
	if c.opts.SolveCaptchas {
		bs["solveCaptchas"] = true
	}
	if o.ContextID != "" {
		bs["context"] = map[string]any{
			"id":      o.ContextID,
			"persist": o.Persist,
		}
	}

	timeout := c.opts.Timeout
	if o.Timeout > 0 {
		timeout = o.Timeout
	}
	// Browserbase uses a structured managed-proxy entry when a country
	// is selected. A bare true enables its default residential proxy.
	proxies, err := resolveProxies(c.opts.Proxies, o)
	if err != nil {
		return "", fmt.Errorf("browserbase: %w", err)
	}
	req := sessionCreateRequest{
		ProjectID:       c.projectID,
		BrowserSettings: bs,
		KeepAlive:       c.opts.KeepAlive,
		Region:          c.opts.Region,
		Timeout:         timeout,
		Proxies:         proxies,
		ExtensionID:     c.opts.ExtensionID,
		UserMetadata:    c.opts.UserMetadata,
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
	httpReq.Header.Set("X-BB-API-Key", c.apiKey)

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

	// Browserbase returns status=RUNNING on success. Anything else means
	// the session failed to start (quota exceeded, invalid region, …).
	if result.Status != "" && result.Status != "RUNNING" {
		return "", fmt.Errorf("session started with status=%q (expected RUNNING)", result.Status)
	}
	if result.ConnectURL == "" {
		return "", fmt.Errorf("no connectUrl in session response (id=%s status=%s)", result.ID, result.Status)
	}
	return result.ConnectURL, nil
}

// fetchDebugURL calls GET /v1/sessions/{id}/debug and returns the
// fullscreen debugger URL. Empty string if the session doesn't expose one.
func (c *Computer) fetchDebugURL() (string, error) {
	if c.sessionID == "" {
		return "", fmt.Errorf("no session id")
	}
	url := fmt.Sprintf("%s/sessions/%s/debug", apiBase, c.sessionID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-BB-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		DebuggerFullscreenURL string `json:"debuggerFullscreenUrl"`
		DebuggerURL           string `json:"debuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.DebuggerFullscreenURL != "" {
		return result.DebuggerFullscreenURL, nil
	}
	return result.DebuggerURL, nil
}

// requestRelease ends the session via the official Browserbase lifecycle:
// POST /v1/sessions/{id} with status=REQUEST_RELEASE. The previous
// implementation used DELETE /v1/sessions/{id}, which the API does not
// expose — so sessions leaked until their idle timeout.
func (c *Computer) requestRelease() error {
	if c.sessionID == "" {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"projectId": c.projectID,
		"status":    "REQUEST_RELEASE",
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/sessions/%s", apiBase, c.sessionID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BB-API-Key", c.apiKey)
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

func (c *Computer) Execute(action computer.Action) ([]byte, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("browserbase: no active session — call browser_session open first")
	}
	switch action.Type {
	case "screenshot":
		return c.Screenshot()

	case "navigate", "back", "reload":
		if err := navigation.Run(c.ctx, action.Type, action.URL, navigateActionTimeout); err != nil {
			return nil, fmt.Errorf("%s: %w", action.Type, err)
		}
		presentation.AfterAction(action.Presentation, 500*time.Millisecond)
		return c.Screenshot()

	case "click":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := c.executeClick(ctx, action, 1, true); err != nil {
			return nil, fmt.Errorf("click: %w", err)
		}
		return c.Screenshot()

	case "double_click":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := c.executeClick(ctx, action, 2, false); err != nil {
			return nil, fmt.Errorf("double_click: %w", err)
		}
		return c.Screenshot()

	case "type":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		delay := time.Duration(action.Presentation.TypingDelayMS) * time.Millisecond
		if err := textinput.TypeWithDelay(ctx, action.Text, "[BROWSERBASE]", delay); err != nil {
			return nil, fmt.Errorf("type: %w", err)
		}
		presentation.AfterAction(action.Presentation, 100*time.Millisecond)
		return c.Screenshot()

	case "key":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := keyinput.Dispatch(ctx, action.Key, "[BROWSERBASE]"); err != nil {
			return nil, fmt.Errorf("key: %w", err)
		}
		presentation.AfterAction(action.Presentation, 100*time.Millisecond)
		return c.Screenshot()

	case "scroll":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := c.scroll(ctx, action); err != nil {
			return nil, fmt.Errorf("scroll: %w", err)
		}
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.Screenshot()

	case "wait":
		dur := action.Duration
		if dur <= 0 {
			dur = 1000
		}
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := sleepWithContext(ctx, time.Duration(dur)*time.Millisecond); err != nil {
			return nil, fmt.Errorf("wait: %w", err)
		}
		return c.Screenshot()

	case "wait_for_stable":
		_, cancel := c.actionContext(action.Type)
		defer cancel()
		if _, err := c.waitForStable(action.QuietMS, action.TimeoutMS); err != nil {
			return nil, fmt.Errorf("wait_for_stable: %w", err)
		}
		return c.Screenshot()

	case "upload_file":
		c.moveToTarget(c.ctx, action)
		res, err := c.uploadFile(action)
		if err != nil {
			return nil, fmt.Errorf("upload_file: %w", err)
		}
		cueSelector := action.Selector
		if cueSelector == "" {
			cueSelector = res.Selector
		}
		c.cueTarget(c.ctx, action, cueSelector, "File uploaded")
		presentation.AfterAction(action.Presentation, 500*time.Millisecond)
		return c.Screenshot()

	case "select_option":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		c.moveToTarget(ctx, action)
		res, err := c.selectOption(ctx, action)
		if err != nil {
			return nil, fmt.Errorf("select_option: %w", err)
		}
		c.cueTarget(ctx, action, res.Selector, "Option selected")
		presentation.AfterAction(action.Presentation, 200*time.Millisecond)
		return c.Screenshot()

	case "set_checked":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		c.moveToTarget(ctx, action)
		res, err := c.setChecked(ctx, action)
		if err != nil {
			return nil, fmt.Errorf("set_checked: %w", err)
		}
		caption := "Unchecked"
		if res.Checked {
			caption = "Checked"
		}
		c.cueTarget(ctx, action, res.Selector, caption)
		presentation.AfterAction(action.Presentation, 150*time.Millisecond)
		return c.Screenshot()

	case "set_temporal":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		c.moveToTarget(ctx, action)
		res, err := c.setTemporal(ctx, action)
		if err != nil {
			return nil, fmt.Errorf("set_temporal: %w", err)
		}
		c.cueTarget(ctx, action, res.Selector, "Date/time set")
		presentation.AfterAction(action.Presentation, 150*time.Millisecond)
		return c.Screenshot()

	case "set_text":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		c.moveToTarget(ctx, action)
		res, err := c.setText(ctx, action)
		if err != nil {
			return nil, fmt.Errorf("set_text: %w", err)
		}
		c.cueTarget(ctx, action, res.Selector, "Text updated")
		presentation.AfterAction(action.Presentation, 150*time.Millisecond)
		return c.Screenshot()

	default:
		return nil, fmt.Errorf("unknown action: %s", action.Type)
	}
}

func (c *Computer) ExecuteAction(action computer.Action) error {
	if c.ctx == nil {
		return fmt.Errorf("browserbase: no active session — call browser_session open first")
	}
	switch action.Type {
	case "click":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := c.executeClick(ctx, action, 1, true); err != nil {
			return fmt.Errorf("click: %w", err)
		}
		return nil
	case "double_click":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		if err := c.executeClick(ctx, action, 2, false); err != nil {
			return fmt.Errorf("double_click: %w", err)
		}
		return nil
	case "wait":
		dur := action.Duration
		if dur <= 0 {
			dur = 1000
		}
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		return sleepWithContext(ctx, time.Duration(dur)*time.Millisecond)
	case "wait_for_stable":
		_, cancel := c.actionContext(action.Type)
		defer cancel()
		_, err := c.waitForStable(action.QuietMS, action.TimeoutMS)
		return err
	case "scroll":
		ctx, cancel := c.actionContext(action.Type)
		defer cancel()
		return c.scroll(ctx, action)
	default:
		return fmt.Errorf("browserbase action-only unsupported for %s", action.Type)
	}
}

// scroll dispatches a real CDP mouseWheel event at (x, y). Browserbase
// pages frequently have nested scroll containers (SaaS dashboards,
// SPAs) where `window.scrollBy` is a no-op; wheel events scroll the
// element under the cursor and fire wheel handlers like a human.
func (c *Computer) scroll(ctx context.Context, a computer.Action) error {
	if a.Label > 0 && a.TargetID == "" {
		if e, ok := c.resolveLabel(a.Label); ok {
			a.X, a.Y = e.Center()
		}
	}
	result, err := scrolltarget.Run(ctx, a, c.display)
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

func (c *Computer) executeClick(ctx context.Context, action computer.Action, clickCount int, focusAfter bool) error {
	x, y := action.X, action.Y
	expectedText := action.ExpectedText
	if action.Selector != "" {
		point, err := selectorclick.Resolve(ctx, action.Selector)
		if err != nil {
			return err
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
	if err := presentation.BeforeClick(ctx, x, y, action.Presentation); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] presentation cursor unavailable, continuing click: %v\n", err)
	}
	if _, err := clickguard.Click(ctx, x, y, clickCount, clickguard.Options{
		ExpectedText: expectedText, RequireExpectedIfDangerous: action.GuardDangerousCoordinate,
	}); err != nil {
		return err
	}
	if focusAfter {
		// Explicit focus at the click point — same rationale as the
		// local package: CDP mouse events don't reliably move DOM
		// focus to form inputs, so later type/insertText can no-op
		// silently. Best-effort; errors ignored so plain clicks on
		// non-focusable elements still succeed.
		focusJS := fmt.Sprintf(`(function(){
			var el = document.elementFromPoint(%d, %d);
			if (el && typeof el.focus === 'function') { el.focus(); return el.tagName; }
			return null;
		})()`, x, y)
		var focusedTag string
		_ = chromedp.Run(ctx, chromedp.Evaluate(focusJS, &focusedTag))
		if focusedTag != "" {
			fmt.Fprintf(os.Stderr, "[BROWSERBASE] click focused <%s>\n", strings.ToLower(focusedTag))
		}
	}
	presentation.AfterAction(action.Presentation, 200*time.Millisecond)
	return nil
}

func (c *Computer) selectOption(ctx context.Context, action computer.Action) (selectinput.Result, error) {
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
	res, err := selectinput.Select(ctx, target, selectinput.Request{
		Text:   action.Text,
		Value:  action.Value,
		Texts:  action.Texts,
		Values: action.Values,
		Mode:   action.Mode,
	})
	if err == nil {
		c.setLastSelectResult(&res)
	}
	return res, err
}

func (c *Computer) setChecked(ctx context.Context, action computer.Action) (checkedinput.Result, error) {
	c.setLastCheckedResult(nil)
	target := checkedinput.Target{Selector: action.Selector}
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
	res, err := checkedinput.Set(ctx, target, checkedinput.Request{Checked: action.Checked})
	if err == nil {
		c.setLastCheckedResult(&res)
	}
	return res, err
}

func (c *Computer) setTemporal(ctx context.Context, action computer.Action) (temporalinput.Result, error) {
	c.setLastTemporalResult(nil)
	target := temporalinput.Target{Selector: action.Selector}
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
	value := action.Value
	if value == "" {
		value = action.Text
	}
	res, err := temporalinput.Set(ctx, target, temporalinput.Request{Value: value})
	if err == nil {
		c.setLastTemporalResult(&res)
	}
	return res, err
}

func (c *Computer) setText(ctx context.Context, action computer.Action) (textinput.SetResult, error) {
	c.setLastTextResult(nil)
	target := textinput.Target{Selector: action.Selector}
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
	text := action.Text
	if text == "" {
		text = action.Value
	}
	res, err := textinput.Set(ctx, target, textinput.SetRequest{Text: text, Mode: action.Mode, NewlineMode: action.NewlineMode})
	if err == nil {
		c.setLastTextResult(&res)
	}
	return res, err
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

func (c *Computer) LastCheckedResult() *checkedinput.Result {
	c.checkedMu.Lock()
	defer c.checkedMu.Unlock()
	return cloneCheckedResult(c.lastCheckedResult)
}

func (c *Computer) setLastCheckedResult(res *checkedinput.Result) {
	c.checkedMu.Lock()
	defer c.checkedMu.Unlock()
	c.lastCheckedResult = cloneCheckedResult(res)
}

func (c *Computer) LastTemporalResult() *temporalinput.Result {
	c.temporalMu.Lock()
	defer c.temporalMu.Unlock()
	return cloneTemporalResult(c.lastTemporalResult)
}

func (c *Computer) setLastTemporalResult(res *temporalinput.Result) {
	c.temporalMu.Lock()
	defer c.temporalMu.Unlock()
	c.lastTemporalResult = cloneTemporalResult(res)
}

func (c *Computer) LastTextResult() *textinput.SetResult {
	c.textMu.Lock()
	defer c.textMu.Unlock()
	return cloneTextResult(c.lastTextResult)
}

func (c *Computer) setLastTextResult(res *textinput.SetResult) {
	c.textMu.Lock()
	defer c.textMu.Unlock()
	c.lastTextResult = cloneTextResult(res)
}

func cloneSelectResult(res *selectinput.Result) *selectinput.Result {
	if res == nil {
		return nil
	}
	clone := *res
	clone.Matched = append([]string(nil), res.Matched...)
	clone.Selected = append([]string(nil), res.Selected...)
	clone.Options = append([]selectinput.Option(nil), res.Options...)
	return &clone
}

func cloneCheckedResult(res *checkedinput.Result) *checkedinput.Result {
	if res == nil {
		return nil
	}
	clone := *res
	return &clone
}

func cloneTemporalResult(res *temporalinput.Result) *temporalinput.Result {
	if res == nil {
		return nil
	}
	clone := *res
	return &clone
}

func cloneTextResult(res *textinput.SetResult) *textinput.SetResult {
	if res == nil {
		return nil
	}
	clone := *res
	clone.Paragraphs = append([]string(nil), res.Paragraphs...)
	return &clone
}

func (c *Computer) uploadFile(action computer.Action) (fileupload.Result, error) {
	if c.sessionID == "" {
		return fileupload.Result{}, fmt.Errorf("browserbase: no active session")
	}
	target := fileupload.Target{Selector: action.Selector}
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
	ctx, cancel := c.actionContext("upload_file")
	defer cancel()
	if payloads, ok := inlineUploadPayloads(action.Files); ok {
		return fileupload.SetPayloads(ctx, target, payloads)
	}
	remoteFiles := make([]string, 0, len(action.Files))
	for _, file := range action.Files {
		remote, err := c.uploadSessionFile(file)
		if err != nil {
			return fileupload.Result{}, err
		}
		remoteFiles = append(remoteFiles, remote)
	}
	return fileupload.SetFiles(ctx, target, remoteFiles)
}

func (c *Computer) cueTarget(
	ctx context.Context,
	action computer.Action,
	selector, caption string,
) {
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
		ctx,
		selector,
		x,
		y,
		hasPoint,
		caption,
		action.Presentation,
	); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] presentation cue unavailable, continuing action: %v\n", err)
	}
}

func (c *Computer) moveToTarget(ctx context.Context, action computer.Action) {
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
		ctx,
		action.Selector,
		x,
		y,
		hasPoint,
		action.Presentation,
	); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] presentation move unavailable, continuing action: %v\n", err)
	}
}

func inlineUploadPayloads(paths []string) ([]fileupload.Payload, bool) {
	payloads := make([]fileupload.Payload, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() > inlineUploadMaxBytes {
			return nil, false
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, false
		}
		mimeType := mime.TypeByExtension(filepath.Ext(path))
		if mimeType == "" {
			mimeType = http.DetectContentType(raw)
		}
		payloads = append(payloads, fileupload.Payload{
			Name: filepath.Base(path),
			MIME: mimeType,
			Data: raw,
		})
	}
	return payloads, true
}

func (c *Computer) uploadSessionFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/sessions/%s/uploads", apiBase, c.sessionID), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-BB-API-Key", c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("browserbase upload: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return "/tmp/.uploads/" + filepath.Base(filePath), nil
}

func (c *Computer) actionContext(action string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.ctx, actionTimeout(action))
}

func actionTimeout(action string) time.Duration {
	switch action {
	case "click", "double_click":
		return clickActionTimeout
	case "type":
		return textActionTimeout
	case "key":
		return keyActionTimeout
	case "scroll":
		return scrollActionTimeout
	case "wait", "wait_for_stable":
		return waitActionTimeout
	case "navigate", "back", "reload":
		return navigateActionTimeout
	case "upload_file":
		return 30 * time.Second
	case "select_option", "set_checked", "set_temporal":
		return 20 * time.Second
	default:
		return 20 * time.Second
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("action_timeout: timed out after %s: %w", d, ctx.Err())
		}
		return ctx.Err()
	}
}

func (c *Computer) Screenshot() ([]byte, error) {
	return c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true})
}

func (c *Computer) ScreenshotWithOptions(options computer.ScreenshotOptions) ([]byte, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("browserbase: no active session — call browser_session open first")
	}
	if options.Annotate {
		c.labelMu.Lock()
		c.lastLabels = nil
		c.labelMu.Unlock()
	}
	c.setLastScreenshotRecovery(nil)
	// Viewport-only screenshot. See local.go for the full rationale:
	// FullScreenshot returns the entire scrollable page, which then
	// either gets aspect-squashed to viewport (silently distorting
	// coordinates) or sent to the LLM at page dimensions that don't
	// match the viewport the agent clicks into. page.CaptureScreenshot
	// returns exactly the visible area at the configured resolution.
	buf, err := c.captureScreenshot()
	if err != nil {
		recovered, info, rerr := c.recoverScreenshotWithFreshTarget(err)
		if rerr != nil {
			return nil, fmt.Errorf("screenshot: %w", err)
		}
		c.setLastScreenshotRecovery(info)
		buf = recovered
	}

	// SoM annotation — same pipeline as local.Screenshot. Any failure
	// returns the raw screenshot.
	if options.Annotate && som.Enabled() {
		var raw json.RawMessage
		if err := chromedp.Run(c.ctx, chromedp.Evaluate(som.EnumScript, &raw)); err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSERBASE] som enum failed: %v\n", err)
			return buf, nil
		}
		elements, err := som.UnmarshalElements(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[BROWSERBASE] som parse failed: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "[BROWSERBASE] som annotate failed: %v\n", aerr)
			return buf, nil
		}
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] som annotated: %d elements\n", len(elements))
		return annotated, nil
	}
	return buf, nil
}

func (c *Computer) LastScreenshotRecovery() *computer.ScreenshotRecoveryInfo {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.lastScreenshotRecover == nil {
		return nil
	}
	cp := *c.lastScreenshotRecover
	return &cp
}

func (c *Computer) setLastScreenshotRecovery(info *computer.ScreenshotRecoveryInfo) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if info == nil {
		c.lastScreenshotRecover = nil
		return
	}
	cp := *info
	c.lastScreenshotRecover = &cp
}

func (c *Computer) recoverScreenshotWithFreshTarget(cause error) ([]byte, *computer.ScreenshotRecoveryInfo, error) {
	if screenshotRecoveryDisabled() {
		return nil, nil, fmt.Errorf("browserbase screenshot recovery disabled")
	}
	if c.ctx == nil {
		return nil, nil, fmt.Errorf("browserbase: no active session")
	}
	currentURL := c.CurrentURL()
	if !strings.HasPrefix(currentURL, "http://") && !strings.HasPrefix(currentURL, "https://") {
		return nil, nil, fmt.Errorf("browserbase screenshot recovery unsupported URL %q", currentURL)
	}
	previousTabID := c.ActiveTabID()
	cc := chromedp.FromContext(c.ctx)
	if cc == nil || cc.Browser == nil {
		return nil, nil, fmt.Errorf("browserbase screenshot recovery: no browser connection")
	}
	browserCtx := cdp.WithExecutor(c.ctx, cc.Browser)

	var newTargetID target.ID
	err := chromedp.Run(c.ctx, chromedp.ActionFunc(func(context.Context) error {
		var createErr error
		newTargetID, createErr = target.CreateTarget(currentURL).Do(browserCtx)
		return createErr
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("browserbase screenshot recovery create target: %w", err)
	}
	if err := c.SwitchTab(string(newTargetID)); err != nil {
		return nil, nil, fmt.Errorf("browserbase screenshot recovery switch target: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	_ = chromedp.Run(waitCtx, chromedp.WaitReady("body", chromedp.ByQuery))
	cancel()
	time.Sleep(500 * time.Millisecond)
	buf, err := c.captureScreenshot()
	if err != nil {
		if previousTabID != "" {
			_ = c.SwitchTab(previousTabID)
		}
		return nil, nil, fmt.Errorf("browserbase screenshot recovery capture: %w", err)
	}
	info := &computer.ScreenshotRecoveryInfo{
		Recovered:     true,
		Strategy:      screenshotRecoveryFreshTarget,
		PreviousTabID: previousTabID,
		ActiveTabID:   string(newTargetID),
		URL:           currentURL,
	}
	if cause != nil {
		info.Cause = cause.Error()
	}
	fmt.Fprintf(os.Stderr, "[BROWSERBASE] screenshot recovered via %s previous_tab=%s active_tab=%s url=%s cause=%v\n",
		info.Strategy, info.PreviousTabID, info.ActiveTabID, info.URL, cause)
	return buf, info, nil
}

func screenshotRecoveryDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APTEVA_BROWSERBASE_SCREENSHOT_RECOVERY"))) {
	case "0", "false", "off", "disabled", "none":
		return true
	default:
		return false
	}
}

func (c *Computer) captureScreenshot() ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= screenshotCaptureAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(screenshotCaptureRetryDelay)
		}
		ctx, cancel := context.WithTimeout(c.ctx, screenshotCaptureTimeout)
		var buf []byte
		err := chromedp.Run(ctx,
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
		cancel()
		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(os.Stderr, "[BROWSERBASE] screenshot capture succeeded on retry %d\n", attempt)
			}
			return buf, nil
		}
		lastErr = err
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] screenshot capture attempt %d/%d failed: %v\n", attempt, screenshotCaptureAttempts, err)
	}
	return nil, lastErr
}

// resolveLabel mirrors local.resolveLabel.
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
			Disabled: e.Disabled, Loading: e.Loading, Dangerous: e.Dangerous, DestructiveEffect: e.DestructiveEffect,
		})
	}
	return out
}

func (c *Computer) DisplaySize() computer.DisplaySize { return c.display }

// SessionInfo implementation
func (c *Computer) SessionType() string { return "browserbase" }
func (c *Computer) SessionID() string   { return c.sessionID }
func (c *Computer) ContextID() string   { return c.contextID }
func (c *Computer) CurrentURL() string {
	var url string
	_ = chromedp.Run(c.ctx, chromedp.Location(&url))
	return url
}

func (c *Computer) ExtractDOM(opts computer.ExtractOptions) (computer.ExtractResult, error) {
	if c.ctx == nil {
		return computer.ExtractResult{}, fmt.Errorf("browserbase: no active session — call browser_session open first")
	}
	return domextract.Run(c.ctx, opts)
}

// DebugURL returns the Browserbase live-view URL for this session, or ""
// if not available. Callers can type-assert against this method to expose
// a "watch live" link in UIs without widening the core Computer interface.
func (c *Computer) DebugURL() string { return c.debugURL }

// OpenSession establishes a session matching opts and (if a URL is
// given) navigates to it. Implements computer.SessionOpener — this is
// the agent-runtime entry point for create-with-context, attach-by-id,
// and rebind-to-different-context. Idempotent when opts match the
// current binding (no-op or just navigate). Otherwise tears down the
// current session before establishing the new one.
func (c *Computer) OpenSession(o computer.OpenOptions) error {
	if o.SessionID != "" && o.ContextID != "" {
		return fmt.Errorf("browserbase: SessionID and ContextID are mutually exclusive")
	}
	if o.SessionID != "" && !o.Environment.IsEmpty() {
		return fmt.Errorf("browserbase: environment settings cannot be applied while attaching to an existing session")
	}
	// Fast path: caller wants the same session/context we already have.
	if c.sessionID != "" {
		sameSession := o.SessionID != "" && o.SessionID == c.sessionID
		sameContext := o.SessionID == "" && o.ContextID != "" && o.ContextID == c.contextID
		if sameSession || sameContext {
			if sameContext {
				c.contextPersist = o.Persist
			}
			c.environment = o.Environment
			if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
				return fmt.Errorf("browserbase environment: %w", err)
			}
			if o.URL != "" {
				return c.navigate(o.URL)
			}
			return nil
		}
		// Different binding — drop the current connection. The previous
		// Browserbase session is left to time out (or release on Close).
		c.releaseCDP()
		c.sessionID = ""
		c.contextID = ""
		c.contextPersist = false
		c.debugURL = ""
	}
	c.usageMu.Lock()
	c.closedSessionID = ""
	c.lastUsage = nil
	c.usageMu.Unlock()

	var connectURL string
	if o.SessionID != "" {
		u, err := c.fetchSessionConnectURL(o.SessionID)
		if err != nil {
			return fmt.Errorf("browserbase: lookup session %s: %w", o.SessionID, err)
		}
		connectURL = u
		c.sessionID = o.SessionID
		c.contextPersist = false
	} else {
		u, err := c.createSession(o)
		if err != nil {
			return fmt.Errorf("browserbase: create session: %w", err)
		}
		connectURL = u
		// c.sessionID is set inside createSession.
		if o.ContextID != "" {
			c.contextID = o.ContextID
			c.contextPersist = o.Persist
		}
	}
	if err := c.establishCDP(connectURL); err != nil {
		return fmt.Errorf("browserbase: connect: %w", err)
	}
	c.environment = o.Environment
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("browserbase environment: %w", err)
	}
	if dbg, derr := c.fetchDebugURL(); derr == nil {
		c.debugURL = dbg
	} else {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] debug URL unavailable: %v\n", derr)
	}
	fmt.Fprintf(os.Stderr, "[BROWSERBASE] session ready id=%s context=%s debug=%s display=%dx%d\n",
		c.sessionID, c.contextID, c.debugURL, c.display.Width, c.display.Height)
	if o.URL != "" {
		return c.navigate(o.URL)
	}
	return nil
}

// Resume is a thin wrapper around OpenSession for callers that hold the
// older Resumable interface. Same code path as OpenSession({SessionID}).
func (c *Computer) Resume(sessionID string) error {
	return c.OpenSession(computer.OpenOptions{SessionID: sessionID})
}

// establishCDP wires up the chromedp remote-allocator + context for the
// given Browserbase connectURL. Tears down on failure.
func (c *Computer) establishCDP(connectURL string) error {
	remoteCtx, remoteCancel := chromedp.NewRemoteAllocator(context.Background(), connectURL,
		chromedp.NoModifyURL)
	browserCtx, browserCancel := chromedp.NewContext(remoteCtx)
	state := chromedp.FromContext(browserCtx)
	browser, err := state.Allocator.Allocate(browserCtx)
	if err != nil {
		browserCancel()
		remoteCancel()
		return err
	}
	state.Browser = browser

	infos, err := target.GetTargets().Do(cdp.WithExecutor(browserCtx, browser))
	if err != nil {
		browserCancel()
		remoteCancel()
		return fmt.Errorf("discover existing page: %w", err)
	}
	pageID := pickInitialPageTarget(infos)
	if pageID == "" {
		browserCancel()
		remoteCancel()
		return fmt.Errorf("browserbase: session has no page target")
	}

	ctx, cancel := chromedp.NewContext(browserCtx, chromedp.WithTargetID(pageID))
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		browserCancel()
		remoteCancel()
		return err
	}
	c.allocCancel = func() {
		browserCancel()
		remoteCancel()
	}
	c.allocCtx = browserCtx
	c.ctx = ctx
	c.attachStabilityTracker()
	c.cancel = cancel
	if err := environment.Apply(c.ctx, c.environment, c.display); err != nil {
		return fmt.Errorf("reapply browser environment: %w", err)
	}
	return nil
}

func (c *Computer) attachStabilityTracker() {
	c.stabilityTracker = nil
	if c.ctx == nil {
		return
	}
	tracker, err := stability.New(c.ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] stability tracker unavailable: %v\n", err)
		return
	}
	c.stabilityTracker = tracker
}

func (c *Computer) waitForStable(quietMS, timeoutMS int) (stability.Result, error) {
	if c.stabilityTracker != nil {
		return c.stabilityTracker.Wait(quietMS, timeoutMS)
	}
	return stability.Wait(c.ctx, quietMS, timeoutMS)
}

func pickInitialPageTarget(infos []*target.Info) target.ID {
	var first, firstNonInternal target.ID
	for _, info := range infos {
		if info == nil || info.Type != "page" || info.Subtype != "" {
			continue
		}
		if first == "" {
			first = info.TargetID
		}
		lowerURL := strings.ToLower(strings.TrimSpace(info.URL))
		if strings.HasPrefix(lowerURL, "http://") || strings.HasPrefix(lowerURL, "https://") {
			return info.TargetID
		}
		if firstNonInternal == "" && lowerURL != "" &&
			!strings.HasPrefix(lowerURL, "about:") &&
			!strings.HasPrefix(lowerURL, "chrome:") &&
			!strings.HasPrefix(lowerURL, "devtools:") {
			firstNonInternal = info.TargetID
		}
	}
	if firstNonInternal != "" {
		return firstNonInternal
	}
	return first
}

// releaseCDP cancels the chromedp context + allocator. Safe to call
// when no session is attached (no-op).
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

// navigate is a small CDP nav wrapper used by OpenSession after a
// successful attach/create.
func (c *Computer) navigate(url string) error {
	if c.ctx == nil {
		return fmt.Errorf("browserbase: no active session — cannot navigate")
	}
	_, err := c.Execute(computer.Action{Type: "navigate", URL: url})
	return err
}

// fetchSessionConnectURL hits GET /v1/sessions/{id} and returns its
// connectUrl, erroring if the session is no longer RUNNING.
func (c *Computer) fetchSessionConnectURL(sessionID string) (string, error) {
	url := fmt.Sprintf("%s/sessions/%s", apiBase, sessionID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-BB-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		ConnectURL string `json:"connectUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Status != "" && result.Status != "RUNNING" {
		return "", fmt.Errorf("session %s is %s, not RUNNING or attachable", sessionID, result.Status)
	}
	if result.ConnectURL == "" {
		return "", fmt.Errorf("session %s has no connectUrl", sessionID)
	}
	return result.ConnectURL, nil
}

func (c *Computer) Close() error {
	// Officially release the session so Browserbase stops billing minutes
	// and has a clean close point for persisting context state. Keep the
	// CDP connection open until after the release request so the session
	// is still active when Browserbase receives REQUEST_RELEASE.
	closingSessionID := c.sessionID
	if closingSessionID != "" {
		c.usageMu.Lock()
		c.closedSessionID = closingSessionID
		c.lastUsage = nil
		c.usageMu.Unlock()
	}
	if err := c.requestRelease(); err != nil {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] release failed id=%s: %v\n", c.sessionID, err)
	} else if c.sessionID != "" {
		fmt.Fprintf(os.Stderr, "[BROWSERBASE] session released id=%s\n", c.sessionID)
	}
	if c.contextID != "" && c.contextPersist {
		// Browserbase documents that context writes are synchronized a
		// few seconds after a persisted session closes. Holding Close
		// until then prevents immediate reopen from racing the sync.
		time.Sleep(5 * time.Second)
	}
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
	c.sessionID = ""
	c.contextID = ""
	c.contextPersist = false
	return nil
}

const (
	usageInitialBackoff = 100 * time.Millisecond
	usageMaxBackoff     = 750 * time.Millisecond
)

// SessionUsage retrieves final Browserbase proxy usage after Close. Close
// preserves the released provider id separately from the active session id so
// it can clear all live browser state without losing the telemetry lookup key.
// Only a terminal provider session is considered final: Browserbase also
// returns proxyBytes while a session is still RUNNING.
func (c *Computer) SessionUsage(ctx context.Context) (computer.SessionUsage, error) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	if c.lastUsage != nil {
		return *c.lastUsage, nil
	}
	sessionID := c.closedSessionID
	if sessionID == "" {
		return computer.SessionUsage{}, fmt.Errorf("browserbase: no closed session is available for usage lookup")
	}

	backoff := usageInitialBackoff
	for {
		usage, terminal, transient, err := c.fetchSessionUsage(ctx, sessionID)
		if err == nil && terminal {
			c.lastUsage = &usage
			return usage, nil
		}
		if err != nil && !transient {
			return computer.SessionUsage{}, err
		}
		if err := waitUsageRetry(ctx, backoff); err != nil {
			if err != context.Canceled && err != context.DeadlineExceeded {
				return computer.SessionUsage{}, err
			}
			return computer.SessionUsage{}, fmt.Errorf("browserbase: final usage unavailable before deadline: %w", err)
		}
		backoff *= 2
		if backoff > usageMaxBackoff {
			backoff = usageMaxBackoff
		}
	}
}

func (c *Computer) fetchSessionUsage(ctx context.Context, sessionID string) (computer.SessionUsage, bool, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/sessions/%s", apiBase, sessionID), nil)
	if err != nil {
		return computer.SessionUsage{}, false, false, err
	}
	req.Header.Set("X-BB-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return computer.SessionUsage{}, false, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("browserbase usage: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		transient := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly ||
			resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return computer.SessionUsage{}, false, transient, err
	}
	var result struct {
		Status     string `json:"status"`
		ProxyBytes *int64 `json:"proxyBytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return computer.SessionUsage{}, false, true, fmt.Errorf("browserbase usage decode: %w", err)
	}
	status := strings.ToUpper(strings.TrimSpace(result.Status))
	terminal := status == "COMPLETED" || status == "ERROR" || status == "TIMED_OUT"
	if !terminal {
		return computer.SessionUsage{}, false, true, nil
	}
	if result.ProxyBytes == nil {
		return computer.SessionUsage{}, false, true, fmt.Errorf("browserbase usage: terminal session omitted proxyBytes")
	}
	return computer.SessionUsage{
		Status:     computer.SessionUsageReady,
		ProxyBytes: result.ProxyBytes,
		MeasuredAt: time.Now().UTC(),
	}, true, false, nil
}

func waitUsageRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
