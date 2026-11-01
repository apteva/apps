package main

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A flow drives a store or beta app through ordinary screen steps. Flows are
// data, so a changed store UI can be handled by passing args.flow instead of
// waiting for a release.
type flowStep struct {
	Do        string    `json:"do"`
	App       string    `json:"app,omitempty"`
	URL       string    `json:"url,omitempty"`
	Match     []matcher `json:"match,omitempty"`
	Optional  bool      `json:"optional,omitempty"`
	TimeoutMS int       `json:"timeout_ms,omitempty"`
	Result    string    `json:"result,omitempty"`
}

type matcher struct {
	Text string `json:"text"`
	Mode string `json:"mode,omitempty"` // equals (default), prefix, contains
}

type flowLog struct {
	Step    int    `json:"step"`
	Do      string `json:"do"`
	Outcome string `json:"outcome"`
	Matched string `json:"matched,omitempty"`
}

var (
	installButtons = []matcher{{Text: "Install"}, {Text: "Update"}}
	openButtons    = []matcher{{Text: "Open"}, {Text: "Play"}}
	storeIDPattern = regexp.MustCompile(`^[0-9]{5,15}$`)
)

func builtinFlow(platform, source string, args map[string]any) ([]flowStep, error) {
	switch {
	case platform == "android":
		// Play serves both public and testing-track builds from the listing
		// once the signed-in account has joined the test.
		return []flowStep{
			{Do: "open_url", URL: "market://details?id={{app_id}}", App: "com.android.vending"},
			{Do: "tap", Match: installButtons, Optional: true, TimeoutMS: 15000, Result: "installing"},
			{Do: "expect", Match: openButtons, TimeoutMS: 4000, Result: "already_installed"},
		}, nil
	case source == "beta":
		steps := []flowStep{}
		if str(args, "store_id") != "" {
			steps = append(steps, flowStep{Do: "open_url", URL: "itms-beta://beta.itunes.apple.com/v1/app/{{store_id}}", App: "com.apple.TestFlight"})
		} else {
			if str(args, "app_name") == "" {
				return nil, problem("invalid_argument", "TestFlight installs need app_name (as shown in TestFlight) or store_id")
			}
			steps = append(steps,
				flowStep{Do: "launch", App: "com.apple.TestFlight"},
				flowStep{Do: "tap", Match: []matcher{{Text: "{{app_name}}"}}, TimeoutMS: 15000})
		}
		return append(steps,
			flowStep{Do: "tap", Match: []matcher{{Text: "Accept"}, {Text: "Start Testing"}}, Optional: true, TimeoutMS: 2500},
			flowStep{Do: "tap", Match: installButtons, Optional: true, TimeoutMS: 15000, Result: "installing"},
			flowStep{Do: "expect", Match: []matcher{{Text: "Open"}}, TimeoutMS: 4000, Result: "already_installed"},
		), nil
	default:
		if str(args, "store_id") == "" {
			return nil, problem("invalid_argument", "App Store installs need store_id (the numeric Apple ID of the app)")
		}
		return []flowStep{
			{Do: "open_url", URL: "itms-apps://apps.apple.com/app/id{{store_id}}", App: "com.apple.AppStore"},
			{Do: "tap", Match: []matcher{{Text: "Get", Mode: "prefix"}, {Text: "Install"}, {Text: "Update"}, {Text: "Redownload"}, {Text: "Re-download"}, {Text: "Download", Mode: "prefix"}}, Optional: true, TimeoutMS: 15000, Result: "installing"},
			{Do: "expect", Match: []matcher{{Text: "Open"}}, TimeoutMS: 4000, Result: "already_installed"},
		}, nil
	}
}

func validateFlow(steps []flowStep) error {
	if len(steps) == 0 || len(steps) > 20 {
		return problem("invalid_argument", "flow must contain 1..20 steps")
	}
	for _, s := range steps {
		switch s.Do {
		case "launch", "open_url", "tap", "expect", "wait":
		default:
			return problem("invalid_argument", "flow steps use launch, open_url, tap, expect or wait")
		}
		if (s.Do == "tap" || s.Do == "expect") && (len(s.Match) == 0 || len(s.Match) > 10) {
			return problem("invalid_argument", "tap and expect need 1..10 match entries")
		}
		for _, m := range s.Match {
			if strings.TrimSpace(m.Text) == "" || len(m.Text) > 200 || (m.Mode != "" && m.Mode != "equals" && m.Mode != "prefix" && m.Mode != "contains") {
				return problem("invalid_argument", "match needs text and mode equals, prefix or contains")
			}
		}
		if s.TimeoutMS < 0 || s.TimeoutMS > 60000 || len(s.Result) > 40 || len(s.URL) > 2048 || len(s.App) > 255 {
			return problem("invalid_argument", "flow step fields are out of range")
		}
	}
	return nil
}

func expandFlow(steps []flowStep, vars map[string]string) []flowStep {
	out := make([]flowStep, len(steps))
	replace := func(s string, escape bool) string {
		for k, v := range vars {
			if escape {
				v = url.PathEscape(v)
			}
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		return s
	}
	for i, s := range steps {
		s.App = replace(s.App, false)
		s.URL = replace(s.URL, true)
		ms := make([]matcher, len(s.Match))
		for j, m := range s.Match {
			m.Text = replace(m.Text, false)
			ms[j] = m
		}
		s.Match = ms
		out[i] = s
	}
	return out
}

func (d *drivers) storeInstall(ctx context.Context, s Session, source string, args map[string]any) (any, error) {
	if source != "store" && source != "beta" {
		return nil, problem("invalid_argument", "source must be artifact, store or beta")
	}
	vars := map[string]string{}
	if v := str(args, "app_id"); v != "" {
		id, err := appID(args)
		if err != nil {
			return nil, err
		}
		vars["app_id"] = id
	} else if s.Device.Platform == "android" {
		return nil, problem("invalid_argument", "app_id (package name) is required")
	}
	if v := str(args, "store_id"); v != "" {
		if !storeIDPattern.MatchString(v) {
			return nil, problem("invalid_argument", "store_id must be the numeric Apple ID")
		}
		vars["store_id"] = v
	}
	if v := strings.TrimSpace(str(args, "app_name")); v != "" {
		if len(v) > 200 {
			return nil, problem("invalid_argument", "app_name is too long")
		}
		vars["app_name"] = v
	}
	var steps []flowStep
	if raw, ok := args["flow"]; ok {
		if err := decodeMap(raw, &steps); err != nil {
			return nil, problem("invalid_argument", "flow must be an array of steps")
		}
	} else {
		var err error
		if steps, err = builtinFlow(s.Device.Platform, source, args); err != nil {
			return nil, err
		}
	}
	if err := validateFlow(steps); err != nil {
		return nil, err
	}
	result, log, err := d.runFlow(ctx, s, expandFlow(steps, vars))
	if err != nil {
		return map[string]any{"source": source, "steps": log}, err
	}
	return map[string]any{"source": source, "result": result, "steps": log, "next": "Poll mobile_app(action=status) or add a wait_for_app test step until installed=true."}, nil
}

func (d *drivers) runFlow(ctx context.Context, s Session, steps []flowStep) (string, []flowLog, error) {
	log := []flowLog{}
	for i, step := range steps {
		entry := flowLog{Step: i + 1, Do: step.Do}
		switch step.Do {
		case "launch":
			if _, err := d.App(ctx, s, "launch", map[string]any{"app_id": step.App}); err != nil {
				return "", append(log, entry), err
			}
			entry.Outcome = "done"
		case "open_url":
			if err := d.openLink(ctx, s, step.URL, step.App); err != nil {
				return "", append(log, entry), err
			}
			entry.Outcome = "done"
		case "wait":
			if err := sleepCtx(ctx, time.Duration(step.TimeoutMS)*time.Millisecond); err != nil {
				return "", log, err
			}
			entry.Outcome = "done"
		case "tap", "expect":
			timeout := time.Duration(step.TimeoutMS) * time.Millisecond
			if timeout == 0 {
				timeout = 10 * time.Second
			}
			el, err := d.waitForElement(ctx, s, step.Match, timeout)
			if err != nil {
				if errorCode(err) == "assertion_failed" && step.Optional {
					entry.Outcome = "skipped"
					log = append(log, entry)
					continue
				}
				return "", append(log, entry), err
			}
			entry.Matched = el.label
			if step.Do == "tap" {
				if err := d.tapPoint(ctx, s, el.cx, el.cy); err != nil {
					return "", append(log, entry), err
				}
				entry.Outcome = "tapped"
			} else {
				entry.Outcome = "found"
			}
			if step.Result != "" {
				return step.Result, append(log, entry), nil
			}
			// Let the next screen render before the following step looks.
			if err := sleepCtx(ctx, 1200*time.Millisecond); err != nil {
				return "", append(log, entry), err
			}
		}
		log = append(log, entry)
	}
	return "done", log, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *drivers) openLink(ctx context.Context, s Session, link, app string) error {
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" || len(link) > 2048 {
		return problem("invalid_argument", "invalid flow URL")
	}
	if !appIDPattern.MatchString(app) {
		return problem("invalid_argument", "open_url needs the app that handles the link")
	}
	if s.Device.Driver == driverXCTest {
		return d.xctestOpenLink(ctx, s, link, app)
	}
	if s.Device.Driver == "adb" {
		out, err := d.shell(ctx, s.Device.UDID, "am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", link, "-p", app)
		if err == nil && (strings.Contains(string(out), "Error") || strings.Contains(string(out), "Exception")) {
			return problem("infrastructure_error", "Android could not open the store page; is "+app+" installed and signed in?")
		}
		return err
	}
	p := map[string]any{"url": link}
	if s.Device.Platform == "ios" {
		p["bundleId"] = app
	} else {
		p["package"] = app
	}
	_, err = d.script(ctx, s, "deepLink", p)
	return err
}

type uiElement struct {
	label  string
	cx, cy int
	score  int
}

var boundsPattern = regexp.MustCompile(`^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$`)

// findElements scans an Android uiautomator or iOS XCUITest source for
// visible elements whose text matches one of ms, best match first.
func findElements(source string, ms []matcher) []uiElement {
	dec := xml.NewDecoder(strings.NewReader(source))
	var found []uiElement
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := map[string]string{}
		for _, a := range start.Attr {
			attr[a.Name.Local] = a.Value
		}
		if attr["visible"] == "false" || attr["enabled"] == "false" {
			continue
		}
		var cx, cy int
		if m := boundsPattern.FindStringSubmatch(attr["bounds"]); m != nil {
			x1, _ := strconv.Atoi(m[1])
			y1, _ := strconv.Atoi(m[2])
			x2, _ := strconv.Atoi(m[3])
			y2, _ := strconv.Atoi(m[4])
			if x2 <= x1 || y2 <= y1 {
				continue
			}
			cx, cy = (x1+x2)/2, (y1+y2)/2
		} else if w, err := strconv.Atoi(attr["width"]); err == nil {
			x, _ := strconv.Atoi(attr["x"])
			y, _ := strconv.Atoi(attr["y"])
			h, _ := strconv.Atoi(attr["height"])
			if w <= 0 || h <= 0 {
				continue
			}
			cx, cy = x+w/2, y+h/2
		} else {
			continue
		}
		for _, field := range []string{"text", "content-desc", "label", "name", "value"} {
			v := strings.TrimSpace(attr[field])
			if v == "" {
				continue
			}
			for mi, m := range ms {
				if !matches(v, m) {
					continue
				}
				score := (len(ms) - mi) * 10
				kind := start.Name.Local + attr["class"] + attr["type"]
				if strings.Contains(kind, "Button") || attr["clickable"] == "true" {
					score += 5
				}
				found = append(found, uiElement{label: v, cx: cx, cy: cy, score: score})
			}
		}
	}
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].score > found[j-1].score; j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
	return found
}

func matches(v string, m matcher) bool {
	a, b := strings.ToLower(v), strings.ToLower(strings.TrimSpace(m.Text))
	switch m.Mode {
	case "prefix":
		return strings.HasPrefix(a, b)
	case "contains":
		return strings.Contains(a, b)
	default:
		return a == b
	}
}

func (d *drivers) waitForElement(ctx context.Context, s Session, ms []matcher, timeout time.Duration) (uiElement, error) {
	deadline := time.Now().Add(timeout)
	for {
		obs, err := d.Observe(ctx, s, true)
		if err != nil {
			return uiElement{}, err
		}
		if found := findElements(obs.Source, ms); len(found) > 0 {
			return found[0], nil
		}
		if time.Now().After(deadline) {
			names := []string{}
			for _, m := range ms {
				names = append(names, m.Text)
			}
			return uiElement{}, problem("assertion_failed", "no visible element matched "+strings.Join(names, " / "))
		}
		if err := sleepCtx(ctx, 700*time.Millisecond); err != nil {
			return uiElement{}, err
		}
	}
}

// tapPoint taps source coordinates: device pixels for ADB, points for Appium
// and the native iOS runner.
func (d *drivers) tapPoint(ctx context.Context, s Session, x, y int) error {
	if s.Device.Driver == driverXCTest {
		return d.xctestTapPoint(ctx, s, x, y)
	}
	if s.Device.Driver == "adb" {
		_, err := d.shell(ctx, s.Device.UDID, "input", "tap", strconv.Itoa(x), strconv.Itoa(y))
		return err
	}
	steps := []any{map[string]any{"type": "pointerMove", "duration": 0, "origin": "viewport", "x": x, "y": y}, map[string]any{"type": "pointerDown", "button": 0}, map[string]any{"type": "pause", "duration": 80}, map[string]any{"type": "pointerUp", "button": 0}}
	_, err := d.sessionWire(ctx, s, "POST", "/actions", map[string]any{"actions": []any{map[string]any{"type": "pointer", "id": "touch", "parameters": map[string]any{"pointerType": "touch"}, "actions": steps}}})
	return err
}

func (d *drivers) appStatus(ctx context.Context, s Session, app string) (any, error) {
	if s.Device.Driver == "adb" {
		out, err := d.shell(ctx, s.Device.UDID, "pm", "path", app)
		if err != nil {
			// pm exits non-zero for a missing package; distinguish an offline phone.
			if _, stateErr := d.adb(ctx, s.Device.UDID, "get-state"); stateErr != nil {
				return nil, stateErr
			}
		}
		return map[string]any{"app_id": app, "installed": strings.Contains(string(out), "package:")}, nil
	}
	key := "appId"
	if s.Device.Platform == "ios" {
		key = "bundleId"
	}
	v, err := d.sessionWire(ctx, s, "POST", "/appium/device/app_installed", map[string]any{key: app})
	if err != nil {
		return nil, err
	}
	installed, ok := v.(bool)
	if !ok {
		return nil, errors.New("Appium returned an invalid install state")
	}
	return map[string]any{"app_id": app, "installed": installed}, nil
}
