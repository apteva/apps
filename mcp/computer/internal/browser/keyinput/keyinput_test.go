package keyinput

import (
	"testing"

	"github.com/chromedp/cdproto/input"
)

func TestEventsRecognizesBrowserCommandKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
		code string
		vk   int64
		mods input.Modifier
	}{
		{name: "tab", key: "Tab", want: "Tab", code: "Tab", vk: 9},
		{name: "backspace", key: "Backspace", want: "Backspace", code: "Backspace", vk: 8},
		{name: "control a", key: "Control+A", want: "a", code: "KeyA", vk: 65, mods: input.ModifierCtrl},
		{name: "ctrl a", key: "CTRL+A", want: "a", code: "KeyA", vk: 65, mods: input.ModifierCtrl},
		{name: "control z", key: "Control+Z", want: "z", code: "KeyZ", vk: 90, mods: input.ModifierCtrl},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, ok, err := Events(tt.key)
			if err != nil {
				t.Fatalf("Events(%q) error: %v", tt.key, err)
			}
			if !ok {
				t.Fatalf("Events(%q) was not recognized", tt.key)
			}
			if len(events) != 2 {
				t.Fatalf("Events(%q) len: want 2, got %d", tt.key, len(events))
			}
			down, up := events[0], events[1]
			if down.Type != input.KeyDown || up.Type != input.KeyUp {
				t.Fatalf("event types: want keyDown/keyUp, got %s/%s", down.Type, up.Type)
			}
			if down.Key != tt.want || up.Key != tt.want {
				t.Fatalf("key: want %q, got down=%q up=%q", tt.want, down.Key, up.Key)
			}
			if down.Code != tt.code || up.Code != tt.code {
				t.Fatalf("code: want %q, got down=%q up=%q", tt.code, down.Code, up.Code)
			}
			if down.WindowsVirtualKeyCode != tt.vk || up.WindowsVirtualKeyCode != tt.vk {
				t.Fatalf("vk: want %d, got down=%d up=%d", tt.vk, down.WindowsVirtualKeyCode, up.WindowsVirtualKeyCode)
			}
			if down.Modifiers != tt.mods || up.Modifiers != tt.mods {
				t.Fatalf("mods: want %d, got down=%d up=%d", tt.mods, down.Modifiers, up.Modifiers)
			}
		})
	}
}

func TestEventsLeavesUnknownStringsAsLiteralFallback(t *testing.T) {
	events, ok, err := Events("not-a-browser-key")
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	if ok || events != nil {
		t.Fatalf("unknown string should fall back to literal typing, got ok=%v events=%#v", ok, events)
	}
}
