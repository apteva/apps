// Package mobile adapts Mobile's public app API. Device drivers and exclusive
// leases remain owned by Mobile, including when it uses a remote runner.
package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"slices"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

type Caller func(context.Context, string, map[string]any, any) error

type device struct {
	ID           string   `json:"id"`
	Platform     string   `json:"platform"`
	Driver       string   `json:"driver"`
	Capabilities []string `json:"capabilities"`
}
type lease struct {
	ID        string `json:"id"`
	Device    device `json:"device"`
	Status    string `json:"status"`
	ExpiresAt int64  `json:"expires_at"`
}

type Computer struct {
	call              Caller
	target            api.SessionTarget
	lease             lease
	display           api.DisplaySize
	request           context.Context
	ttl               int
	startupWindow     time.Duration
	startupRetryDelay time.Duration
	closed            bool
}

func New(target api.SessionTarget, call Caller) (*Computer, error) {
	if call == nil {
		return nil, errors.New("mobile: app bridge unavailable")
	}
	return &Computer{call: call, target: target, ttl: 900}, nil
}
func (c *Computer) ctx() context.Context {
	if c.request != nil {
		return c.request
	}
	return context.Background()
}
func (c *Computer) BindRequest(ctx context.Context) func() {
	prev := c.request
	c.request = ctx
	return func() { c.request = prev }
}
func (c *Computer) invoke(ctx context.Context, tool string, args map[string]any, out any) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.call(bounded, tool, args, out)
}
func (c *Computer) OpenSession(opts api.OpenOptions) error {
	if opts.SessionID != "" || opts.ContextID != "" || opts.Proxy != nil || opts.ExternalProxy != nil || !opts.Environment.IsEmpty() {
		return errors.New("mobile: provider attach, browser profiles, proxy and emulation are unsupported")
	}
	if opts.Timeout > 0 {
		c.ttl = opts.Timeout
	}
	if c.ttl < 30 || c.ttl > 3600 {
		return errors.New("mobile: timeout must be between 30 and 3600 seconds")
	}
	args := map[string]any{"action": "open", "ttl_seconds": c.ttl, "mode": "screen"}
	if c.target.DeviceID != "" {
		args["device_id"] = c.target.DeviceID
	}
	if c.target.Platform != "" {
		args["platform"] = c.target.Platform
	}
	if c.target.Physical != nil {
		args["physical"] = *c.target.Physical
	}
	if c.target.Mode != "" {
		args["mode"] = c.target.Mode
	}
	var result struct {
		Session lease `json:"session"`
	}
	if err := c.openLease(args, &result); err != nil {
		return err
	}
	c.lease = result.Session
	if c.lease.ID == "" {
		return errors.New("mobile: open returned no session id")
	}
	c.closed = false
	if c.lease.Status != "active" || c.lease.Device.ID == "" || (c.target.DeviceID != "" && c.target.DeviceID != c.lease.Device.ID) || (c.target.Platform != "" && c.target.Platform != c.lease.Device.Platform) {
		return errors.Join(errors.New("mobile: returned device does not match requested target"), c.Close())
	}
	c.target.Kind = "mobile"
	c.target.DeviceID = c.lease.Device.ID
	c.target.Platform = c.lease.Device.Platform
	if _, err := c.Screenshot(); err != nil {
		return errors.Join(err, c.Close())
	}
	if c.target.AppID != "" {
		if err := c.app("launch", c.target.AppID); err != nil {
			return errors.Join(err, c.Close())
		}
	}
	if opts.URL != "" {
		if err := c.ExecuteAction(api.Action{Type: "navigate", URL: opts.URL}); err != nil {
			return errors.Join(err, c.Close())
		}
	}
	return nil
}
func (c *Computer) ensureLease() error {
	if c.closed || c.lease.ID == "" {
		return errors.New("mobile: session is closed")
	}
	// Only renew while the current lease is still live. Never resurrect an
	// expired device reservation, which another caller may already own.
	if time.Now().Unix() >= c.lease.ExpiresAt {
		return fmt.Errorf("mobile: %w", api.ErrSessionExpired)
	}
	if time.Until(time.Unix(c.lease.ExpiresAt, 0)) > time.Duration(c.ttl)*time.Second/2 {
		return nil
	}
	var out struct {
		Session lease `json:"session"`
	}
	if err := c.invoke(c.ctx(), "mobile_session", map[string]any{"action": "renew", "session_id": c.lease.ID, "ttl_seconds": c.ttl}, &out); err != nil {
		return err
	}
	if out.Session.ID != c.lease.ID || out.Session.Status != "active" || out.Session.ExpiresAt <= time.Now().Unix() {
		return errors.New("mobile: invalid renewed lease")
	}
	c.lease = out.Session
	return nil
}
func (c *Computer) Screenshot() ([]byte, error) {
	if err := c.ensureLease(); err != nil {
		return nil, err
	}
	var out sdk.MCPResult
	if err := c.invoke(c.ctx(), "mobile_observe", map[string]any{"session_id": c.lease.ID}, &out); err != nil {
		return nil, err
	}
	if out.IsError {
		return nil, errors.New("mobile: observation failed")
	}
	for _, content := range out.Content {
		if content.Type != "image" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(content.Data)
		if err != nil {
			return nil, err
		}
		if len(data) > 12<<20 {
			return nil, errors.New("mobile: screenshot exceeds 12 MiB")
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("mobile: invalid screenshot: %w", err)
		}
		if config.Width <= 0 || config.Height <= 0 {
			return nil, errors.New("mobile: invalid screen dimensions")
		}
		c.display = api.DisplaySize{Width: config.Width, Height: config.Height}
		return data, nil
	}
	return nil, errors.New("mobile: observation returned no image")
}
func (c *Computer) Execute(a api.Action) ([]byte, error) {
	if err := c.ExecuteAction(a); err != nil {
		return nil, err
	}
	if a.NoScreenshot {
		return nil, nil
	}
	return c.Screenshot()
}
func (c *Computer) ExecuteAction(a api.Action) error {
	if !slices.Contains(c.Capabilities().Actions, a.Type) {
		return fmt.Errorf("mobile: unsupported action %q", a.Type)
	}
	if err := c.ensureLease(); err != nil {
		return err
	}
	if a.Type == "screenshot" {
		_, err := c.Screenshot()
		return err
	}
	if a.Selector != "" || a.Label != 0 || a.TargetID != "" || a.ExpectedText != "" || a.ExpectedName != "" || a.ExpectedRole != "" || a.ExpectedEffect != "" || a.ConfirmConsequence != "" || len(a.WorkflowConstraints) > 0 {
		return errors.New("mobile: DOM targets and semantic consequence/workflow guards are unsupported; inspect the screen and use coordinates")
	}
	args := map[string]any{"session_id": c.lease.ID}
	action := a.Type
	switch action {
	case "wait":
		if a.Duration < 0 || a.Duration > 30000 {
			return errors.New("mobile: wait must be 0..30000 ms")
		}
		timer := time.NewTimer(time.Duration(a.Duration) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-c.ctx().Done():
			return c.ctx().Err()
		case <-timer.C:
			return nil
		}
	case "click", "swipe", "scroll":
		// Refresh dimensions before input, accounting for device rotation since
		// the caller's last observation. Reject stale geometry instead of guessing.
		previous := c.display
		if _, err := c.Screenshot(); err != nil {
			return err
		}
		if previous != c.display {
			return errors.New("mobile: screen dimensions changed; take a fresh screenshot before input")
		}
		x, y := a.X, a.Y
		if action == "scroll" && x == 0 && y == 0 {
			x = c.display.Width / 2
			y = c.display.Height / 2
		}
		nx, ny, err := c.coordinate(x, y)
		if err != nil {
			return err
		}
		args["x"], args["y"] = nx, ny
		if action == "click" {
			action = "tap"
		} else {
			tx, ty := a.ToX, a.ToY
			if action == "scroll" {
				amount := a.Amount
				if amount <= 0 {
					amount = 300
				}
				tx, ty = x, y
				switch a.Direction {
				case "down":
					ty -= amount
				case "up":
					ty += amount
				case "right":
					tx -= amount
				case "left":
					tx += amount
				default:
					return errors.New("mobile: invalid scroll direction")
				}
				tx = max(0, min(c.display.Width-1, tx))
				ty = max(0, min(c.display.Height-1, ty))
			}
			ex, ey, err := c.coordinate(tx, ty)
			if err != nil {
				return err
			}
			args["to_x"], args["to_y"] = ex, ey
			action = "swipe"
		}
	case "type":
		action = "text"
		args["text"] = a.Text
	case "key":
		key := strings.ToLower(a.Key)
		if !slices.Contains(c.Capabilities().Keys, key) {
			return fmt.Errorf("mobile: unsupported key %q", a.Key)
		}
		args["key"] = key
	case "home":
		action = "key"
		args["key"] = "home"
	case "navigate":
		args["url"] = a.URL
	case "launch_app", "stop_app":
		return c.app(strings.TrimSuffix(a.Type, "_app"), a.AppID)
	}
	args["action"] = action
	var result any
	if err := c.invoke(c.ctx(), "mobile_action", args, &result); err != nil {
		return err
	}
	// Native Mobile cannot report a verified current browser URL. Do not expose
	// the requested destination as an observed URL.
	return nil
}
func (c *Computer) app(action, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("mobile: app_id required")
	}
	var out any
	return c.invoke(c.ctx(), "mobile_app", map[string]any{"session_id": c.lease.ID, "action": action, "app_id": id}, &out)
}
func (c *Computer) coordinate(x, y int) (float64, float64, error) {
	if x < 0 || y < 0 || x >= c.display.Width || y >= c.display.Height {
		return 0, 0, errors.New("mobile: coordinate outside observed screen")
	}
	return float64(x) / float64(c.display.Width), float64(y) / float64(c.display.Height), nil
}
func (c *Computer) Close() error {
	if c.closed || c.lease.ID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out any
	err := c.invoke(ctx, "mobile_session", map[string]any{"action": "close", "session_id": c.lease.ID}, &out)
	if err == nil {
		c.closed = true
	}
	return err
}
func (c *Computer) DisplaySize() api.DisplaySize { return c.display }
func (c *Computer) SessionType() string          { return "mobile" }
func (c *Computer) SessionID() string            { return c.lease.ID }
func (c *Computer) CurrentURL() string           { return "" }
func (c *Computer) Target() api.SessionTarget    { return c.target }
func (c *Computer) Capabilities() api.Capabilities {
	actions := []string{"screenshot", "click", "swipe", "scroll", "type", "key", "home", "navigate", "wait", "launch_app", "stop_app"}
	if c.supportsNativeScreen() {
		actions = append(actions, "native_assert", "native_wait", "native_tap", "native_tap_badged", "native_tap_if_present", "native_type", "native_type_secure", "native_replace")
	}
	keys := []string{"home", "enter"}
	if c.lease.Device.Platform == "android" {
		keys = append(keys, "back")
	}
	return api.Capabilities{TargetKind: "mobile", CoordinateSpace: "screen_pixels", Actions: actions, Keys: keys, Observations: []string{"screenshot"}, Context: api.ContextCapabilities{Reuse: true, Persistence: true}}
}
func (c *Computer) ProviderSessionState(ctx context.Context) (api.ProviderSessionState, error) {
	return api.ProviderSessionState{Status: c.lease.Status, ExpiresAt: time.Unix(c.lease.ExpiresAt, 0).UTC().Format(time.RFC3339)}, nil
}

var _ api.Computer = (*Computer)(nil)
