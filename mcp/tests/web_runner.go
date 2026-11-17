package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
	// Page diagnostics (Computer 0.14.0+): rules for this check, the read
	// cursor, and everything recorded so far.
	diagnostics bool
	rules       diagnosticRules
	cursor      int64
	recorded    BrowserDiagnostics
}

// computerFeatures records what the bound Computer supports. It is probed once
// per run.
type computerFeatures struct {
	probed, reads bool
}

const computerReadVersionMessage = "computer 0.14.0 or newer required for text assertions"

// computerSupportsReads reports whether Computer has read_element, page
// diagnostics, masked screenshots and profile purge (Computer 0.14.0+).
// Computer reports no version for regular sessions, so Tests calls
// browser_diagnostics with an empty session: 0.14.0+ answers "session_id
// required", while older versions do not have the tool.
func computerSupportsReads(ctx context.Context, app *sdk.AppCtx) bool {
	var out map[string]any
	err := callComputer(ctx, app, "browser_diagnostics", map[string]any{"session_id": ""}, &out)
	return err != nil && strings.Contains(err.Error(), "session_id required")
}

func (r *appRunContext) computerReads(ctx context.Context) bool {
	if !r.features.probed {
		r.features.probed = true
		r.features.reads = computerSupportsReads(ctx, r.ctx)
	}
	return r.features.reads
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
		return nil, &computerError{text: fmt.Sprintf("%s failed: %s", args["action"], firstLine(err.Error())), cause: err}
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

// computerError shows a short message and keeps Computer's full error for
// callers that need its details.
type computerError struct {
	text  string
	cause error
}

func (e *computerError) Error() string { return e.text }
func (e *computerError) Unwrap() error { return e.cause }

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
	// The session opens on about:blank so the check's first navigate step is a
	// real URL change. Opening on the destination made that step a same-URL
	// navigation, which Computer rejects when the app rewrites its URL hash.
	args := map[string]any{"action": "open", "viewport": map[string]any{"width": config.Width, "height": config.Height}}
	rules, diagnostics := resolveDiagnosticRules(check.Definition.Diagnostics, app.targets)
	diagnostics = diagnostics && app.computerReads(ctx)
	if diagnostics {
		args["diagnostics"] = true
	}
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
	app.web = &webSession{id: opened.SessionID, currentURL: opened.CurrentURL, diagnostics: diagnostics, rules: rules}
	if useSession && app.setupCheck {
		app.sessionContext = opened.AppContextID
	}
	return app.web, nil
}

var navigationRemained = regexp.MustCompile(`navigation_ineffective: browser remained at "([^"]*)"`)

// ineffectiveNavigationURL extracts the URL from Computer's
// navigation_ineffective error.
func ineffectiveNavigationURL(err error) (string, bool) {
	text := err.Error()
	var full *computerError
	if errors.As(err, &full) {
		text = full.cause.Error()
	}
	match := navigationRemained.FindStringSubmatch(text)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// sameIgnoringFragment compares two URLs with their #… part removed.
func sameIgnoringFragment(a, b string) bool {
	normalize := func(raw string) (string, bool) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return "", false
		}
		u.Fragment, u.RawFragment = "", ""
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		if u.Path == "" {
			u.Path = "/"
		}
		return u.String(), true
	}
	left, ok1 := normalize(a)
	right, ok2 := normalize(b)
	return ok1 && ok2 && left == right
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

// readElement fills an observation from Computer's read_element: /visible is
// true when any match is visible; /text, /value, /checked and /attributes come
// from the first match. Text or values Computer withheld set /redacted.
func readElement(ctx context.Context, app *appRunContext, selector string, observation map[string]any) error {
	out, err := computerUse(ctx, app, map[string]any{"action": "read_element", "selector": selector, "all": true})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(out)
	var read struct {
		Count    int `json:"count"`
		Elements []struct {
			Text          string            `json:"text"`
			Value         *string           `json:"value"`
			ValueRedacted bool              `json:"value_redacted"`
			Checked       *bool             `json:"checked"`
			Visible       bool              `json:"visible"`
			Redacted      bool              `json:"redacted"`
			Attributes    map[string]string `json:"attributes"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(raw, &read); err != nil {
		return errors.New("Computer returned an unreadable element read")
	}
	visible := false
	for _, element := range read.Elements {
		visible = visible || element.Visible
	}
	observation["count"] = float64(read.Count) // compared like a JSON number
	observation["visible"] = visible
	if len(read.Elements) == 0 {
		return nil
	}
	first := read.Elements[0]
	redacted := first.Redacted || first.ValueRedacted
	if !first.Redacted {
		observation["text"] = first.Text
	}
	if first.Value != nil && !first.ValueRedacted && !first.Redacted {
		observation["value"] = *first.Value
	}
	if first.Checked != nil {
		observation["checked"] = *first.Checked
	}
	if len(first.Attributes) > 0 && !first.Redacted {
		attributes := make(map[string]any, len(first.Attributes))
		for name, value := range first.Attributes {
			attributes[name] = value
		}
		observation["attributes"] = attributes
	}
	if redacted {
		observation["redacted"] = true
	}
	return nil
}

// collectWebDiagnostics reads page problems recorded since the last read and
// returns the first rule they break, or "".
func (a *App) collectWebDiagnostics(ctx context.Context, app *appRunContext) string {
	web := app.web
	if web == nil || !web.diagnostics {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var out struct {
		Enabled       *bool         `json:"enabled"`
		Exceptions    []string      `json:"exceptions"`
		ConsoleErrors int           `json:"console_errors"`
		Requests      []pageRequest `json:"requests"`
		Truncated     bool          `json:"truncated"`
		Cursor        int64         `json:"cursor"`
	}
	if err := callComputer(ctx, app.ctx, "browser_diagnostics", map[string]any{"session_id": web.id, "since": web.cursor}, &out); err != nil {
		return "page diagnostics could not be read: " + firstLine(err.Error())
	}
	if out.Enabled != nil && !*out.Enabled {
		// This backend records nothing; the check runs without diagnostics.
		web.diagnostics = false
		web.recorded.Unavailable = true
		return ""
	}
	if out.Cursor > web.cursor {
		web.cursor = out.Cursor
	}
	rec := &web.recorded
	rec.Exceptions = append(rec.Exceptions, out.Exceptions...)
	rec.ConsoleErrors += out.ConsoleErrors
	rec.Truncated = rec.Truncated || out.Truncated
	problem := ""
	for _, req := range out.Requests {
		if raw, err := json.Marshal(req); err == nil && len(rec.Requests) < 200 {
			rec.Requests = append(rec.Requests, raw)
		}
		if problem == "" && web.rules.failedRequest(req) {
			problem = describeRequest(req)
		}
	}
	switch {
	case web.rules.exceptions && len(out.Exceptions) > 0:
		return "page exception: " + firstLine(out.Exceptions[0])
	case problem != "":
		return problem
	case web.rules.console && out.ConsoleErrors > 0:
		return fmt.Sprintf("page logged %d console error(s)", out.ConsoleErrors)
	case web.rules.truncated && out.Truncated:
		return "page diagnostics were truncated, so problems may be missing"
	}
	return ""
}

// screenshotSecrets returns up to 20 credential values for masking, longest
// first (Computer accepts at most 20).
func screenshotSecrets(secrets []string) []string {
	out := append([]string{}, secrets...)
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
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
	if action.Action == "observe" && !app.computerReads(ctx) {
		for _, assertion := range step.Assertions {
			if isReadPath(assertion.Path) {
				return nil, errors.New(computerReadVersionMessage)
			}
		}
	}
	switch action.Action {
	case "navigate":
		target, err := targetURL(app, action.Target, path)
		if err != nil {
			return nil, err
		}
		if _, err = computerUse(ctx, app, map[string]any{"action": "navigate", "url": target}); err != nil {
			// Navigating to the page already shown, when the app adds a URL
			// hash on load, reads as "no URL change" to Computer. Accept it
			// when the browser is on the destination apart from the hash.
			remained, ok := ineffectiveNavigationURL(err)
			if !ok || !sameIgnoringFragment(target, remained) {
				return nil, err
			}
			app.web.currentURL = remained
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
		case selector != "" && app.computerReads(ctx):
			if err := readElement(ctx, app, selector, observation); err != nil {
				return nil, err
			}
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
	evidence := &BrowserEvidence{SessionID: session.id, Protocol: "computer/regular", Diagnostics: session.recorded}
	if result.Status != "passed" && len(steps) > 0 {
		last := &steps[len(steps)-1]
		masked := app.computerReads(ctx)
		if session.typedValue && !masked {
			last.Diagnostics = map[string]any{"screenshot": "skipped because this check entered a credential"}
		} else {
			var shot struct {
				PNG string `json:"png_b64"`
			}
			args := map[string]any{"session_id": session.id}
			if masked {
				// Computer hides password and sensitive fields, every form field
				// when inputs is set, and any text containing a secret. It
				// captures nothing if masking fails.
				args["mask"] = map[string]any{"inputs": session.typedValue, "secrets": screenshotSecrets(app.secrets)}
			}
			err := callComputer(ctx, app.ctx, "browser_screenshot", args, &shot)
			if err != nil && masked {
				last.Diagnostics = map[string]any{"screenshot_skipped": "mask_failed"}
			}
			if err == nil && shot.PNG != "" {
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

// releaseWebContext deletes the run's saved sign-in context. With Computer
// 0.14.0+ it also purges the local Chrome profile (cookies and storage),
// retrying once if a session still held it. It returns a warning, not an
// error: a leftover context never fails the run.
func (a *App) releaseWebContext(ctx context.Context, app *appRunContext) string {
	if app.sessionContext == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	id := app.sessionContext
	app.sessionContext = ""
	args := map[string]any{"id": id}
	purge := app.computerReads(ctx)
	if purge {
		args["purge_profile"] = true
	}
	var out map[string]any
	err := callComputer(ctx, app.ctx, "computer_context_delete", args, &out)
	if err != nil && purge && strings.Contains(err.Error(), "applies only to local contexts") {
		// Hosted browser providers keep their own profiles; just unlink.
		delete(args, "purge_profile")
		purge = false
		err = callComputer(ctx, app.ctx, "computer_context_delete", args, &out)
	}
	if err != nil && purge {
		// A session may still be closing; give it a moment and retry once.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		err = callComputer(ctx, app.ctx, "computer_context_delete", args, &out)
	}
	if err != nil {
		return fmt.Sprintf("sign-in browser context %s was not deleted: %s", id, firstLine(err.Error()))
	}
	return ""
}

func decodeBase64(text string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(text)
}
