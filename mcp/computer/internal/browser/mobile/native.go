package mobile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

var ErrNativeScreenMarkerAbsent = errors.New("native screen marker is not visible")

// NativeWait polls only observations. A timed-out transition never replays input.
func (c *Computer) NativeWait(app string, required []string, timeoutMS int) error {
	if timeoutMS < 1 || timeoutMS > 28000 {
		return errors.New("native_wait duration must be 1–28000 milliseconds")
	}
	ctx, cancel := context.WithTimeout(c.ctx(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	defer c.BindRequest(ctx)()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := c.NativeScreen("native_assert", app, required, "", "")
		if ctx.Err() != nil {
			return fmt.Errorf("waiting for native screen: %w", ctx.Err())
		}
		if !errors.Is(err, ErrNativeScreenMarkerAbsent) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for native screen: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// NativeScreen checks the iOS accessibility tree before dispatching an exact
// element action. Keep the tree inside Computer because it can contain private text.
func (c *Computer) NativeScreen(action, app string, required []string, target, text string) error {
	if c.lease.Device.Platform == "android" {
		_, err := c.androidNative(action, app, required, target, text, nil)
		return err
	}
	if !c.supportsNativeScreen() {
		return errors.New("native screen checks require the iOS XCTest driver")
	}
	if strings.TrimSpace(app) == "" || len(required) == 0 || len(required) > 12 {
		return errors.New("native screen requires an app label and 1–12 screen markers")
	}
	for _, marker := range required {
		if strings.TrimSpace(marker) == "" || len(marker) > 500 {
			return errors.New("native screen marker must contain 1–500 bytes")
		}
	}
	secure := action == "native_type_secure"
	replace := action == "native_replace"
	badged := action == "native_tap_badged"
	typing := action == "native_type" || secure || replace
	switch action {
	case "native_assert":
		if target != "" || text != "" {
			return errors.New("native_assert accepts screen markers only")
		}
	case "native_tap", "native_tap_badged", "native_tap_if_present", "native_type", "native_type_secure", "native_replace":
		if strings.TrimSpace(target) == "" || len(target) > 500 {
			return errors.New("native input requires an exact element label or identifier")
		}
		if typing && (text == "" || len(text) > 256 || strings.ContainsAny(text, "\r\n\t\b")) {
			return errors.New("native typing requires 1–256 bytes of single-line text")
		}
		if !typing && text != "" {
			return errors.New("native tap does not accept text")
		}
	default:
		return errors.New("unknown native screen action")
	}

	source, err := c.nativeSource()
	if err != nil {
		return err
	}
	element, err := matchNativeScreen(source, app, required, target, badged)
	if err != nil || action == "native_assert" {
		return err
	}
	if typing {
		if err := nativeTextField(element, target, secure, replace); err != nil {
			return err
		}
	}

	var result any
	args := map[string]any{"session_id": c.lease.ID, "action": "tap_element", "using": "accessibility id", "value": element.target}
	if element.hasLabelChild {
		// The native iOS driver only supports exact accessibility IDs. When a
		// label repeats within a button, tap the verified button's bounds.
		x, y, err := element.center()
		if err != nil {
			return err
		}
		args = map[string]any{"session_id": c.lease.ID, "action": "tap", "x": x, "y": y}
	}
	if err := c.invoke(c.ctx(), "mobile_action", args, &result); err != nil {
		return err
	}
	if typing {
		source, err = c.nativeSource()
		if err != nil {
			return err
		}
		element, err = matchNativeScreen(source, app, required, target, false)
		if err != nil || nativeTextField(element, target, secure, replace) != nil {
			return errors.New("native text field changed after focusing it")
		}
		input := text
		if replace && element.value != target && element.value != element.label {
			input = strings.Repeat("\b", utf8.RuneCountInString(element.value)) + text
		}
		err = c.invoke(c.ctx(), "mobile_action", map[string]any{
			"session_id": c.lease.ID, "action": "text", "text": input,
		}, &result)
		if secure && err != nil {
			return errors.New("native secure input failed")
		}
		if secure {
			return c.verifyNativeSecureInput(app, required, target, text)
		}
		if replace && err == nil {
			source, err = c.nativeSource()
			if err != nil {
				return err
			}
			field, matchErr := matchNativeScreen(source, app, required, target, false)
			if matchErr != nil || field.kind != element.kind || field.value != text {
				return errors.New("native replacement could not be verified")
			}
		}
		return err
	}
	return nil
}

func (c *Computer) verifyNativeSecureInput(app string, required []string, target, text string) error {
	source, err := c.nativeSource()
	if err == nil {
		field, matchErr := matchNativeScreen(source, app, required, target, false)
		if matchErr == nil && field.kind == "SecureTextField" && field.value != "" &&
			(field.value == text || strings.Trim(field.value, "•●*") == "") {
			return nil
		}
	}
	return errors.New("native secure field could not be verified after typing")
}

func nativeTextField(element nativeElement, target string, secure, replace bool) error {
	if secure {
		if element.kind != "SecureTextField" {
			return errors.New("native secure input requires a secure text field")
		}
	} else if replace {
		if (element.kind != "TextField" && element.kind != "TextView") || len(element.value) > 256 {
			return errors.New("native replacement requires a regular text field or text view with at most 256 bytes")
		}
		return nil
	} else if element.kind != "TextField" && element.kind != "TextView" {
		return errors.New("native_type target is not a text field")
	}
	if element.value != "" && element.value != target && element.value != element.label {
		return errors.New("native typing target already contains text")
	}
	return nil
}

func (c *Computer) supportsNativeScreen() bool {
	if c.lease.Device.Platform == "android" {
		return c.lease.Device.Driver == "adb" && slices.Contains(c.lease.Device.Capabilities, "source")
	}
	return c.lease.Device.Platform == "ios" && c.lease.Device.Driver == "xctest" &&
		slices.Contains(c.lease.Device.Capabilities, "source") && slices.Contains(c.lease.Device.Capabilities, "elements")
}

func (c *Computer) nativeSource() (string, error) {
	if err := c.ensureLease(); err != nil {
		return "", err
	}
	var out sdk.MCPResult
	args := map[string]any{"session_id": c.lease.ID, "source": true}
	if c.lease.Device.Platform == "android" && slices.Contains(c.lease.Device.Capabilities, "source_only") {
		args["image"] = false
	}
	if err := c.invoke(c.ctx(), "mobile_observe", args, &out); err != nil {
		return "", err
	}
	source, _ := out.StructuredContent["source"].(string)
	if out.IsError || source == "" || len(source) > 4<<20 {
		return "", errors.New("native UI source is missing or exceeds its limit")
	}
	return source, nil
}

type nativeElement struct {
	kind, name, label, value        string
	target                          string
	visible, enabled, hasLabelChild bool
	x, y, width, height             int
	screenWidth, screenHeight       int
}

func (e nativeElement) center() (float64, float64, error) {
	if e.x < 0 || e.y < 0 || e.width <= 0 || e.height <= 0 ||
		e.screenWidth <= 0 || e.screenHeight <= 0 ||
		e.x > e.screenWidth-e.width || e.y > e.screenHeight-e.height {
		return 0, 0, errors.New("native button bounds are outside the screen")
	}
	return (float64(e.x) + float64(e.width)/2) / float64(e.screenWidth),
		(float64(e.y) + float64(e.height)/2) / float64(e.screenHeight), nil
}

func (e nativeElement) sameButtonBounds(other nativeElement) bool {
	return e.kind == "Button" && other.kind == "Button" && e.name == other.name && e.label == other.label &&
		e.visible && e.enabled && other.visible && other.enabled && e.width > 0 && e.height > 0 &&
		e.x == other.x && e.y == other.y && e.width == other.width && e.height == other.height
}

func matchNativeScreen(source, app string, required []string, target string, badged bool) (nativeElement, error) {
	d := xml.NewDecoder(strings.NewReader(source))
	found := make([]bool, len(required))
	var match nativeElement
	depth, roots, matches := 0, 0, 0
	buttonDepth := 0
	screenWidth, screenHeight := 0, 0
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return match, errors.New("invalid native UI source")
		}
		switch tag := t.(type) {
		case xml.EndElement:
			if depth == buttonDepth {
				buttonDepth = 0
			}
			depth--
		case xml.StartElement:
			depth++
			node := nativeElement{kind: strings.TrimPrefix(tag.Name.Local, "XCUIElementType"), x: -1, y: -1}
			for _, attr := range tag.Attr {
				switch attr.Name.Local {
				case "name":
					node.name = attr.Value
				case "label":
					node.label = attr.Value
				case "value":
					node.value = attr.Value
				case "visible":
					node.visible = attr.Value == "true"
				case "enabled":
					node.enabled = attr.Value == "true"
				case "x":
					if n, err := strconv.Atoi(attr.Value); err == nil {
						node.x = n
					}
				case "y":
					if n, err := strconv.Atoi(attr.Value); err == nil {
						node.y = n
					}
				case "width":
					node.width, _ = strconv.Atoi(attr.Value)
				case "height":
					node.height, _ = strconv.Atoi(attr.Value)
				}
			}
			if depth == 1 {
				roots++
				if roots != 1 || node.kind != "Application" || (node.name != app && node.label != app) {
					return match, errors.New("unexpected foreground app")
				}
				screenWidth, screenHeight = node.width, node.height
				continue
			}
			if node.target = nativeTargetMatch(node, target, badged); node.target != "" {
				if buttonDepth > 0 && (node.kind == "Other" || node.kind == "StaticText") {
					match.hasLabelChild = true
				} else if matches == 1 && match.sameButtonBounds(node) {
					// Some iOS views expose the same button twice. Tap its
					// verified bounds instead of passing an ambiguous ID.
					match.hasLabelChild = true
				} else {
					matches++
					match = node
					match.screenWidth, match.screenHeight = screenWidth, screenHeight
					if node.kind == "Button" {
						buttonDepth = depth
					}
				}
			}
			if node.visible {
				for i, marker := range required {
					found[i] = found[i] || node.name == marker || node.label == marker || node.value == marker
				}
			}
		}
	}
	if roots != 1 || depth != 0 {
		return match, errors.New("incomplete native UI source")
	}
	for i, present := range found {
		if !present {
			return match, fmt.Errorf("%w (marker %d)", ErrNativeScreenMarkerAbsent, i+1)
		}
	}
	if target != "" && (matches != 1 || !match.visible || !match.enabled) {
		return match, errors.New("native target must match one visible, enabled element")
	}
	if badged && match.kind != "Button" {
		return match, errors.New("native badged target must be a button")
	}
	return match, nil
}

func nativeTargetMatch(node nativeElement, target string, badged bool) string {
	if target == "" {
		return ""
	}
	for _, text := range []string{node.label, node.name} {
		// Keep the base label exact. The comma-space boundary prevents a
		// longer account handle from matching a shorter one.
		if text == target || (badged && strings.HasPrefix(text, target+", ") && len(text) > len(target)+2) {
			return text
		}
	}
	return ""
}
