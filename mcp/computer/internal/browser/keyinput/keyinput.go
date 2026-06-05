// Package keyinput normalizes agent key strings into real CDP key events.
package keyinput

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

type keySpec struct {
	Key, Code string
	VK        int
}

// specialKeys maps human-typed key names to their CDP descriptor.
// Names are case-insensitive on lookup.
var specialKeys = map[string]keySpec{
	"escape":     {"Escape", "Escape", 27},
	"esc":        {"Escape", "Escape", 27},
	"enter":      {"Enter", "Enter", 13},
	"return":     {"Enter", "Enter", 13},
	"tab":        {"Tab", "Tab", 9},
	"backspace":  {"Backspace", "Backspace", 8},
	"delete":     {"Delete", "Delete", 46},
	"del":        {"Delete", "Delete", 46},
	"space":      {" ", "Space", 32},
	"arrowup":    {"ArrowUp", "ArrowUp", 38},
	"up":         {"ArrowUp", "ArrowUp", 38},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40},
	"down":       {"ArrowDown", "ArrowDown", 40},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37},
	"left":       {"ArrowLeft", "ArrowLeft", 37},
	"arrowright": {"ArrowRight", "ArrowRight", 39},
	"right":      {"ArrowRight", "ArrowRight", 39},
	"home":       {"Home", "Home", 36},
	"end":        {"End", "End", 35},
	"pageup":     {"PageUp", "PageUp", 33},
	"pgup":       {"PageUp", "PageUp", 33},
	"pagedown":   {"PageDown", "PageDown", 34},
	"pgdn":       {"PageDown", "PageDown", 34},
	"f1":         {"F1", "F1", 112},
	"f2":         {"F2", "F2", 113},
	"f3":         {"F3", "F3", 114},
	"f4":         {"F4", "F4", 115},
	"f5":         {"F5", "F5", 116},
	"f6":         {"F6", "F6", 117},
	"f7":         {"F7", "F7", 118},
	"f8":         {"F8", "F8", 119},
	"f9":         {"F9", "F9", 120},
	"f10":        {"F10", "F10", 121},
	"f11":        {"F11", "F11", 122},
	"f12":        {"F12", "F12", 123},
}

func parseModifier(s string) (input.Modifier, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "alt", "option", "opt":
		return input.ModifierAlt, true
	case "ctrl", "control":
		return input.ModifierCtrl, true
	case "cmd", "meta", "command", "super", "win":
		return input.ModifierMeta, true
	case "shift":
		return input.ModifierShift, true
	}
	return 0, false
}

// Dispatch sends key as a browser command when it is a named key or a
// modifier combo such as "Control+A". Unknown multi-character strings
// intentionally fall back to chromedp.KeyEvent for backwards compatibility.
func Dispatch(ctx context.Context, key, logPrefix string) error {
	events, ok, err := Events(key)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "%s key fallback (unknown key %q): typing literally\n", logPrefix, key)
		return chromedp.Run(ctx, chromedp.KeyEvent(key))
	}
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, ev := range events {
			if err := ev.Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

// Events returns the CDP key events for recognised browser-command keys.
// ok=false means callers should preserve legacy literal typing behavior.
func Events(key string) ([]*input.DispatchKeyEventParams, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("empty key")
	}
	mods := input.ModifierNone
	keyName := key

	if strings.Contains(key, "+") && len(key) > 1 {
		parts := strings.Split(key, "+")
		for _, m := range parts[:len(parts)-1] {
			bit, ok := parseModifier(m)
			if !ok {
				return nil, false, nil
			}
			mods |= bit
		}
		keyName = strings.TrimSpace(parts[len(parts)-1])
	}

	if spec, ok := specialKeys[strings.ToLower(keyName)]; ok {
		return keyEvents(spec.Key, spec.Code, int64(spec.VK), mods), true, nil
	}

	if len(keyName) == 1 {
		if mods == input.ModifierNone {
			return nil, false, nil
		}
		code := ""
		ch := keyName[0]
		switch {
		case ch >= 'a' && ch <= 'z':
			code = "Key" + strings.ToUpper(keyName)
		case ch >= 'A' && ch <= 'Z':
			code = "Key" + keyName
		case ch >= '0' && ch <= '9':
			code = "Digit" + keyName
		}
		vk := int64(strings.ToUpper(keyName)[0])
		return keyEvents(strings.ToLower(keyName), code, vk, mods), true, nil
	}

	return nil, false, nil
}

func keyEvents(key, code string, vk int64, mods input.Modifier) []*input.DispatchKeyEventParams {
	down := input.DispatchKeyEvent(input.KeyDown).
		WithKey(key).WithCode(code).
		WithWindowsVirtualKeyCode(vk).
		WithModifiers(mods)
	up := input.DispatchKeyEvent(input.KeyUp).
		WithKey(key).WithCode(code).
		WithWindowsVirtualKeyCode(vk).
		WithModifiers(mods)
	return []*input.DispatchKeyEventParams{down, up}
}
