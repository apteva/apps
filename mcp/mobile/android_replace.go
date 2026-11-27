package main

import (
	"context"
	"encoding/xml"
	"errors"
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
	if err = matchAndroidFocusedFieldDetails(obs.Source, pkg, resource, text, kind, bounds); err != nil {
		return nil, problem("verification_failed", "Android replacement was dispatched but the resulting field value was not confirmed")
	}
	return map[string]any{"ok": true, "replaced": true, "verified": true}, nil
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

func matchAndroidFocusedFieldDetails(source, pkg, resource, value, kind, bounds string) error {
	d := xml.NewDecoder(strings.NewReader(source))
	matches, roots, depth := 0, 0, 0
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return problem("invalid_state", "invalid Android UI source")
		}
		switch tag := t.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if tag.Name.Local != "hierarchy" {
					return problem("invalid_state", "unexpected Android UI source")
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
			if depth == 2 && a["package"] != pkg {
				return problem("invalid_state", "unexpected foreground app")
			}
			if a["focused"] == "true" {
				if a["package"] != pkg || a["resource-id"] != resource || a["text"] != value || (kind != "" && a["class"] != kind) || (bounds != "" && a["bounds"] != bounds) || a["password"] == "true" || a["enabled"] != "true" || a["visible-to-user"] == "false" || !editableClass(a["class"]) {
					return problem("invalid_state", "focused Android field does not match the expected regular text field")
				}
				matches++
			}
		case xml.EndElement:
			depth--
		}
	}
	if roots != 1 || depth != 0 || matches != 1 {
		return problem("invalid_state", "replacement requires one focused Android text field")
	}
	return nil
}
