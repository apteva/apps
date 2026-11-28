package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ADB replacement is opt-in and confined to the exact, focused regular field
// observed by the caller. Validate the new bytes before changing any input.
func (d *drivers) adbReplaceText(ctx context.Context, s Session, args map[string]any, text string) (any, error) {
	pkg, resource := str(args, "expected_package"), str(args, "expected_resource_id")
	kind, bounds := str(args, "expected_class"), str(args, "expected_bounds")
	current, supplied := args["expected_current"].(string)
	if !appIDPattern.MatchString(pkg) || (resource == "" && (kind != "android.widget.EditText" || bounds == "")) || len(resource) > 500 || len(bounds) > 100 || !supplied || len(current) > 4096 || len(text) > 4096 {
		return nil, problem("invalid_argument", "replacement requires the expected package, resource ID and current text")
	}
	for _, r := range text {
		if r < 32 && r != '\t' {
			return nil, problem("invalid_argument", "replacement text cannot contain control characters")
		}
	}
	obs, err := d.Observe(ctx, s, true)
	if err != nil {
		return nil, err
	}
	if err = matchAndroidFocusedFieldDetails(obs.Source, pkg, resource, current, kind, bounds); err != nil {
		return nil, err
	}
	if !asciiInput(text) {
		// Unicode and % cannot be typed by `input text`; set the field's
		// value through accessibility instead. Verification is unchanged.
		if err = d.adbSetText(ctx, s.Device.UDID, text); err != nil {
			return nil, problem("infrastructure_error", "Android text replacement failed; inspect the field before retrying")
		}
		return d.verifyReplacement(ctx, s, pkg, resource, text, kind, bounds)
	}
	commands := [][]string{{"input", "keycombination", "113", "29"}, {"input", "keyevent", "67"}}
	if text != "" {
		commands = append(commands, []string{"input", "text", strings.ReplaceAll(text, " ", "%s")})
	}
	for _, command := range commands {
		out, e := d.shell(ctx, s.Device.UDID, command...)
		if e != nil {
			return nil, problem("infrastructure_error", "Android text replacement failed; inspect the field before retrying")
		}
		if strings.Contains(string(out), "Error") || strings.Contains(string(out), "Exception") || strings.Contains(string(out), "Unknown") {
			return nil, problem("infrastructure_error", "Android rejected text replacement; inspect the field before retrying")
		}
	}
	return d.verifyReplacement(ctx, s, pkg, resource, text, kind, bounds)
}

func (d *drivers) verifyReplacement(ctx context.Context, s Session, pkg, resource, text, kind, bounds string) (any, error) {
	obs, err := d.Observe(ctx, s, true)
	if err != nil {
		return nil, err
	}
	// The field may wrap the new text and change height; match its shape,
	// not its exact box (see matchFocusedField).
	got, err := matchFocusedField(obs.Source, pkg, resource, text, kind, bounds, true)
	if err != nil {
		return nil, problem("verification_failed", "Android replacement was dispatched but the resulting field value was not confirmed")
	}
	return map[string]any{"ok": true, "replaced": true, "verified": true, "bounds": got}, nil
}

// editableClass accepts EditText and the framework's EditText subclasses
// (AutoCompleteTextView backs most search boxes).
func editableClass(class string) bool {
	for _, suffix := range []string{"EditText", "AutoCompleteTextView", "SearchView$SearchAutoComplete"} {
		if strings.HasSuffix(class, suffix) {
			return true
		}
	}
	return false
}

func matchAndroidFocusedField(source, pkg, resource, value string) error {
	return matchAndroidFocusedFieldDetails(source, pkg, resource, value, "", "")
}

// matchAndroidFocusedFieldDetails requires the exact box the caller observed:
// it proves the field about to be edited is the one the caller picked.
func matchAndroidFocusedFieldDetails(source, pkg, resource, value, kind, bounds string) error {
	_, err := matchFocusedField(source, pkg, resource, value, kind, bounds, false)
	return err
}

// resizeTolerance is how far a resized field's left and right edges may move.
const resizeTolerance = 2

type box struct{ l, t, r, b int }

func parseBox(s string) (box, bool) {
	var v box
	if n, err := fmt.Sscanf(s, "[%d,%d][%d,%d]", &v.l, &v.t, &v.r, &v.b); err != nil || n != 4 || fmt.Sprintf("[%d,%d][%d,%d]", v.l, v.t, v.r, v.b) != s {
		return box{}, false
	}
	return v, true
}

// resizedFrom accepts a field that wrapped text after a replacement: the same
// left and right edges, a positive height no taller than its window, and a
// vertical range that overlaps the old one. That covers boxes that grow down,
// up (pinned above the keyboard) or both ways (centered dialogs); focus
// moving to another field puts it somewhere else on screen and fails.
func resizedFrom(was, now box, window *box) bool {
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	h := now.b - now.t
	if abs(now.l-was.l) > resizeTolerance || abs(now.r-was.r) > resizeTolerance || h <= 0 || now.r <= now.l {
		return false
	}
	if window != nil && window.b > window.t && h > window.b-window.t {
		return false
	}
	return now.t < was.b && was.t < now.b
}

// matchFocusedField checks there is exactly one focused regular text field
// in the foreground package with the expected ID, class and text, and returns
// its bounds. With resized=false the box must equal bounds exactly. With
// resized=true (after a replacement) a field with a resource ID needs no box,
// and an anonymous one may have changed height as resizedFrom allows.
func matchFocusedField(source, pkg, resource, value, kind, bounds string, resized bool) (string, error) {
	var was box
	if resized && resource == "" && bounds != "" {
		var ok bool
		if was, ok = parseBox(bounds); !ok {
			return "", problem("invalid_argument", "invalid expected bounds")
		}
	}
	var window *box
	found := ""
	d := xml.NewDecoder(strings.NewReader(source))
	matches, roots, depth := 0, 0, 0
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", problem("invalid_state", "invalid Android UI source")
		}
		switch tag := t.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if tag.Name.Local != "hierarchy" {
					return "", problem("invalid_state", "unexpected Android UI source")
				}
				roots++
			}
			if tag.Name.Local != "node" {
				continue
			}
			a := map[string]string{}
			for _, attr := range tag.Attr {
				a[attr.Name.Local] = attr.Value
			}
			if depth == 2 {
				if a["package"] != pkg {
					return "", problem("invalid_state", "unexpected foreground app")
				}
				if v, ok := parseBox(a["bounds"]); ok && window == nil {
					window = &v
				}
			}
			if a["focused"] == "true" {
				boxOK := bounds == "" || a["bounds"] == bounds
				if resized && resource != "" {
					boxOK = true
				} else if resized && bounds != "" {
					now, ok := parseBox(a["bounds"])
					boxOK = ok && resizedFrom(was, now, window)
				}
				if a["package"] != pkg || a["resource-id"] != resource || a["text"] != value || (kind != "" && a["class"] != kind) || !boxOK || a["password"] == "true" || a["enabled"] != "true" || a["visible-to-user"] == "false" || !editableClass(a["class"]) {
					return "", problem("invalid_state", "focused Android field does not match the expected regular text field")
				}
				matches++
				found = a["bounds"]
			}
		case xml.EndElement:
			depth--
		}
	}
	if roots != 1 || depth != 0 || matches != 1 {
		return "", problem("invalid_state", "replacement requires one focused Android text field")
	}
	return found, nil
}
