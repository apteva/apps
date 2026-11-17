package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Existing-mode browser checks drive Computer's regular browser tools
// (browser_session, computer_use, browser_screenshot). Unlike the isolated
// test browser, these sessions are not network-restricted, report no console
// or request diagnostics, and do not mask screenshots.

type webSession struct {
	id         string
	currentURL string
	typedValue bool // a credential or sensitive value was entered in this check
}

func (a *App) webBinding(ctx context.Context, app *sdk.AppCtx) error {
	var out map[string]any
	if err := callComputer(ctx, app, "computer_context_list", map[string]any{}, &out); err != nil {
		return errors.New("a bound Computer app is required for browser checks")
	}
	return nil
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// computerUse calls computer_use and treats both a Go error and a
// success:false reply as a failure.
func computerUse(ctx context.Context, app *appRunContext, args map[string]any) (map[string]any, error) {
	args["session_id"] = app.web.id
	var out map[string]any
	if err := callComputer(ctx, app.ctx, "computer_use", args, &out); err != nil {
		return nil, fmt.Errorf("%s failed: %s", args["action"], firstLine(err.Error()))
	}
	if failed, _ := out["failed"].(bool); failed || out["success"] == false {
		code, _ := out["error_code"].(string)
		if code == "" {
			code = "rejected"
		}
		return nil, fmt.Errorf("%s failed: %s", args["action"], code)
	}
	if current, ok := out["current_url"].(string); ok && current != "" {
		app.web.currentURL = current
	}
	return out, nil
}

func firstLine(text string) string {
	text, _, _ = strings.Cut(text, "\n")
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func (a *App) openWebCheck(ctx context.Context, check Check, app *appRunContext) (*webSession, error) {
	target := app.snapshot.Target
	if target == nil || len(app.targets) == 0 {
		return nil, errNoTarget
	}
	config := target.Browser
	if config == nil {
		config = &BrowserConfig{}
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(app.targets))
	for name := range app.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	start := app.targets[names[0]].String()
	for _, step := range check.Definition.Steps {
		if step.Browser != nil && step.Browser.Action == "navigate" {
			if u, err := targetURL(app, step.Browser.Target, step.Browser.Path); err == nil {
				start = u
			}
			break
		}
	}
	args := map[string]any{"action": "open", "url": start, "viewport": map[string]any{"width": config.Width, "height": config.Height}}
	session := app.snapshot.Session
	useSession := session != nil && session.Kind == "browser" && !app.sessionOff
	if useSession {
		if app.setupCheck {
			app.sessionContext = ""
			args["context_name"] = fmt.Sprintf("tests-run-%d-%s", app.runID, randomSuffix())
			args["auto_create_context"] = true
		} else if app.sessionContext != "" {
			args["context_id"] = app.sessionContext
		}
	}
	var opened struct {
		SessionID    string `json:"session_id"`
		CurrentURL   string `json:"current_url"`
		AppContextID string `json:"app_context_id"`
	}
	if err := callComputer(ctx, app.ctx, "browser_session", args, &opened); err != nil || !strings.HasPrefix(opened.SessionID, "br_") {
		return nil, errors.New("Computer could not open a browser session")
	}
	app.web = &webSession{id: opened.SessionID, currentURL: opened.CurrentURL}
	if useSession && app.setupCheck {
		app.sessionContext = opened.AppContextID
	}
	return app.web, nil
}

func targetURL(app *appRunContext, name, path string) (string, error) {
	base := app.targets[name]
	if base == nil {
		return "", fmt.Errorf("unknown target %q", name)
	}
	u := *base
	p, query, _ := strings.Cut(path, "?")
	u.Path = strings.TrimSuffix(u.Path, "/") + p
	u.RawQuery = query
	return u.String(), nil
}

// cssSelector maps a Tests locator to a CSS selector for Computer.
func cssSelector(l BrowserLocator) string {
	switch {
	case l.Selector != "":
		return l.Selector
	case l.TestID != "":
		return `[data-testid="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(l.TestID) + `"]`
	}
	return ""
}

func (a *App) executeWebStep(ctx context.Context, step Step, values map[string]any, app *appRunContext, result *StepResult) (any, error) {
	action := *step.Browser
	path, err := expand(action.Path, values, nil)
	if err != nil {
		return nil, err
	}
	var value string
	sensitive := action.Sensitive
	if action.Value != nil {
		if value, err = expand(*action.Value, values, nil); err != nil {
			return nil, err
		}
	}
	if action.ValueRef != "" {
		var ok bool
		if value, ok = app.credentials[action.ValueRef]; !ok {
			return nil, errors.New("browser value reference is unavailable")
		}
		sensitive = true
	}
	selector := cssSelector(action.Locator)
	observation := map[string]any{"action_dispatched": false}
	finish := func() map[string]any {
		if u, err := url.Parse(app.web.currentURL); err == nil {
			observation["path"] = u.EscapedPath()
			observation["url"] = u.Scheme + "://" + u.Host + u.EscapedPath()
			for name, base := range app.targets {
				if base.Scheme == u.Scheme && base.Host == u.Host && strings.HasPrefix(u.Path, base.Path) {
					observation["target"] = name
				}
			}
		}
		return observation
	}
	wait := func(conditions []map[string]any, timeout int) (bool, error) {
		out, err := computerUse(ctx, app, map[string]any{"action": "wait_for", "conditions": conditions, "timeout_ms": timeout})
		if err != nil {
			return false, err
		}
		matched, _ := out["matched"].(bool)
		return matched, nil
	}
	// Wait for the element before a mutating action, then dispatch once.
	ready := func() error {
		deadline, ok := ctx.Deadline()
		timeout := 10000
		if ok {
			timeout = int(time.Until(deadline).Milliseconds())
		}
		if timeout < 500 {
			timeout = 500
		}
		if timeout > 30000 {
			timeout = 30000
		}
		matched, err := wait([]map[string]any{{"type": "selector_present", "selector": selector}}, timeout)
		if err != nil {
			return err
		}
		if !matched {
			return errors.New("browser target is not visible before the deadline")
		}
		return nil
	}
	dispatch := func(args map[string]any) error {
		if err := ready(); err != nil {
			return err
		}
		args["selector"] = selector
		out, err := computerUse(ctx, app, args)
		if err != nil {
			return err
		}
		result.ActionDispatched = true
		observation["action_dispatched"] = true
		if !sensitive {
			for _, key := range []string{"checked", "current_value", "text_value", "verified"} {
				if v, ok := out[key]; ok {
					observation[key] = v
				}
			}
		}
		return nil
	}
	switch action.Action {
	case "navigate":
		target, err := targetURL(app, action.Target, path)
		if err != nil {
			return nil, err
		}
		if _, err = computerUse(ctx, app, map[string]any{"action": "navigate", "url": target}); err != nil {
			return nil, err
		}
		result.ActionDispatched = true
		observation["action_dispatched"] = true
	case "reload":
		if _, err = computerUse(ctx, app, map[string]any{"action": "navigate", "url": app.web.currentURL}); err != nil {
			return nil, err
		}
		result.ActionDispatched = true
		observation["action_dispatched"] = true
	case "observe":
		switch {
		case selector != "":
			matched, err := wait([]map[string]any{{"type": "selector_present", "selector": selector}}, 500)
			if err != nil {
				return nil, err
			}
			observation["visible"] = matched
		case action.Locator.Text != "":
			matched, err := wait([]map[string]any{{"type": "text_present", "value": action.Locator.Text, "case_sensitive": true}}, 500)
			if err != nil {
				return nil, err
			}
			observation["visible"] = matched
		}
	case "click":
		err = dispatch(map[string]any{"action": "click"})
	case "fill":
		app.web.typedValue = app.web.typedValue || sensitive
		err = dispatch(map[string]any{"action": "set_text", "text": value})
	case "select":
		err = dispatch(map[string]any{"action": "select_option", "value": value})
	case "check":
		err = dispatch(map[string]any{"action": "set_checked", "checked": action.Checked})
	case "upload":
		file, ok := app.snapshot.Target.Browser.Uploads[action.UploadRef]
		if !ok {
			return nil, errors.New("browser upload fixture is unavailable")
		}
		err = dispatch(map[string]any{"action": "upload_file", "base64": file.Base64, "filename": file.Name, "mime_type": file.MIME})
	default:
		return nil, errors.New("unsupported browser action")
	}
	if err != nil {
		return nil, err
	}
	return finish(), nil
}

// closeWebCheck captures failure evidence and closes the session. Screenshots
// are skipped when the check typed a credential, since regular sessions do not
// mask input values.
func (a *App) closeWebCheck(ctx context.Context, app *appRunContext, result *Result, steps []StepResult) (*BrowserEvidence, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	session := app.web
	evidence := &BrowserEvidence{SessionID: session.id, Protocol: "computer/regular"}
	if result.Status != "passed" && len(steps) > 0 {
		last := &steps[len(steps)-1]
		if session.typedValue {
			last.Diagnostics = map[string]any{"screenshot": "skipped because this check entered a credential"}
		} else {
			var shot struct {
				PNG string `json:"png_b64"`
			}
			if err := callComputer(ctx, app.ctx, "browser_screenshot", map[string]any{"session_id": session.id}, &shot); err == nil && shot.PNG != "" {
				if raw, err := decodeBase64(shot.PNG); err == nil {
					sum := sha256.Sum256(raw)
					if id, err := saveBrowserArtifact(app.ctx, app.runID, result.CheckID, last.ID, browserArtifact{MIME: "image/png", SHA256: hex.EncodeToString(sum[:]), Base64: shot.PNG}); err == nil {
						last.ArtifactID = id
					}
				}
			}
		}
	}
	var closed map[string]any
	err := callComputer(ctx, app.ctx, "browser_session", map[string]any{"action": "close", "session_id": session.id}, &closed)
	if err != nil {
		return evidence, errors.New("browser session could not be closed")
	}
	return evidence, nil
}

// releaseWebContext unlinks the run's saved sign-in context.
func (a *App) releaseWebContext(ctx context.Context, app *appRunContext) {
	if app.sessionContext == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var out map[string]any
	_ = callComputer(ctx, app.ctx, "computer_context_delete", map[string]any{"id": app.sessionContext}, &out)
	app.sessionContext = ""
}

func decodeBase64(text string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(text)
}
