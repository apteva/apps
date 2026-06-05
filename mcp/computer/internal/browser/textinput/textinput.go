// Package textinput handles CDP text entry with special cases for native date/time controls.
package textinput

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

type focusedElement struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
}

var (
	hm24Re     = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(?::(\d{2}))?$`)
	hm12Re     = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?(?::(\d{2}))?\s*([ap])\.?m\.?$`)
	weekValue  = regexp.MustCompile(`^\d{4}-W\d{2}$`)
	monthValue = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

// Type inserts text into the focused target. Normal text fields use
// Input.insertText. Native date/time controls are special: Chrome's
// insertText can ignore selected sub-fields, so full temporal values are set
// through the DOM with input/change events and partial edits use key events.
func Type(ctx context.Context, text, logPrefix string) error {
	elem, _ := activeElement(ctx)
	if temporalInput(elem.Type) {
		if value, ok := NormalizeTemporalValue(elem.Type, text); ok {
			if err := setActiveValue(ctx, value); err == nil {
				return nil
			} else {
				fmt.Fprintf(os.Stderr, "%s temporal set failed (%v), falling back to key events\n", logPrefix, err)
			}
		}
		return chromedp.Run(ctx, chromedp.KeyEvent(text))
	}

	err := chromedp.Run(ctx, input.InsertText(text))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s insertText failed (%v), falling back to KeyEvent\n", logPrefix, err)
		if err := chromedp.Run(ctx, chromedp.KeyEvent(text)); err != nil {
			return fmt.Errorf("type: %w", err)
		}
	}
	return nil
}

func activeElement(ctx context.Context) (focusedElement, error) {
	var elem focusedElement
	err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		var el = document.activeElement;
		if (!el) return {tag:"", type:""};
		return {tag: String(el.tagName || "").toLowerCase(), type: String(el.type || "").toLowerCase()};
	})()`, &elem))
	return elem, err
}

func setActiveValue(ctx context.Context, value string) error {
	var actual string
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	expr := fmt.Sprintf(`(function(){
		var value = %s;
		var el = document.activeElement;
		if (!el || !("value" in el)) throw new Error("active element has no value");
		var setter = Object.getOwnPropertyDescriptor(el.constructor.prototype, "value");
		if (setter && setter.set) setter.set.call(el, value);
		else el.value = value;
		el.dispatchEvent(new InputEvent("input", {bubbles:true, inputType:"insertText"}));
		el.dispatchEvent(new Event("change", {bubbles:true}));
		return el.value;
	})()`, string(encoded))
	err = chromedp.Run(ctx, chromedp.Evaluate(expr, &actual))
	if err != nil {
		return err
	}
	if actual != value {
		return fmt.Errorf("browser normalized value to %q", actual)
	}
	return nil
}

func temporalInput(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "date", "time", "datetime-local", "month", "week":
		return true
	default:
		return false
	}
}

// NormalizeTemporalValue converts common human-entered values into the string
// format native HTML temporal inputs expect. ok=false means this is a partial
// edit, so callers should send normal key events instead.
func NormalizeTemporalValue(kind, raw string) (value string, ok bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	switch kind {
	case "time":
		return normalizeTime(raw)
	case "date":
		return normalizeDate(raw)
	case "datetime-local":
		return normalizeDateTime(raw)
	case "month":
		return normalizeMonth(raw)
	case "week":
		if weekValue.MatchString(raw) {
			return raw, true
		}
		return "", false
	default:
		return "", false
	}
}

func normalizeTime(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, ".", "")
	s = strings.Join(strings.Fields(s), " ")
	if m := hm12Re.FindStringSubmatch(s); m != nil {
		hour, minute, second := atoi(m[1]), atoiDefault(m[2], 0), atoiDefault(m[3], -1)
		if hour < 1 || hour > 12 || minute > 59 || second > 59 {
			return "", false
		}
		if m[4] == "p" && hour != 12 {
			hour += 12
		}
		if m[4] == "a" && hour == 12 {
			hour = 0
		}
		return formatTimeValue(hour, minute, second), true
	}
	if m := hm24Re.FindStringSubmatch(s); m != nil && strings.Contains(s, ":") {
		hour, minute, second := atoi(m[1]), atoiDefault(m[2], 0), atoiDefault(m[3], -1)
		if hour > 23 || minute > 59 || second > 59 {
			return "", false
		}
		return formatTimeValue(hour, minute, second), true
	}
	return "", false
}

func normalizeDate(raw string) (string, bool) {
	for _, layout := range []string{
		"2006-01-02", "01/02/2006", "1/2/2006", "Jan 2 2006", "January 2 2006", "Jan 2, 2006", "January 2, 2006",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

func normalizeDateTime(raw string) (string, bool) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("2006-01-02T15:04"), true
	}
	for _, sep := range []string{"T", " "} {
		if !strings.Contains(raw, sep) {
			continue
		}
		parts := strings.SplitN(raw, sep, 2)
		date, dok := normalizeDate(parts[0])
		tm, tok := normalizeTime(parts[1])
		if dok && tok {
			return date + "T" + tm[:5], true
		}
	}
	return "", false
}

func normalizeMonth(raw string) (string, bool) {
	if monthValue.MatchString(raw) {
		return raw, true
	}
	for _, layout := range []string{"Jan 2006", "January 2006"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01"), true
		}
	}
	return "", false
}

func formatTimeValue(hour, minute, second int) string {
	if second >= 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		n = n*10 + int(ch-'0')
	}
	return n
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	return atoi(s)
}
