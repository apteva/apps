package main

import (
	"context"
	"strings"
	"testing"
)

func TestAndroidReplaceChecksFocusBeforeMutation(t *testing.T) {
	for _, mode := range []string{"good", "clear", "wrong_app", "wrong_value", "password", "not_focused", "failed_readback", "unicode"} {
		t.Run(mode, func(t *testing.T) {
			source := `<hierarchy><node package="com.example" enabled="true"><node package="com.example" resource-id="bio" class="android.widget.EditText" text="old" focused="true" enabled="true"/></node></hierarchy>`
			switch mode {
			case "wrong_app":
				source = strings.ReplaceAll(source, "com.example", "com.other")
			case "wrong_value":
				source = strings.ReplaceAll(source, `text="old"`, `text="different"`)
			case "password":
				source = strings.ReplaceAll(source, `focused="true"`, `focused="true" password="true"`)
			case "not_focused":
				source = strings.ReplaceAll(source, `focused="true"`, `focused="false"`)
			}
			d := newDrivers(runnerConfig{})
			mutations := []string{}
			d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				cmd := args[len(args)-1]
				if len(args) > 2 && args[2] == "exec-out" {
					return testPNG(), nil
				}
				if strings.Contains(cmd, "'cat'") {
					return []byte(source), nil
				}
				if strings.Contains(cmd, "'input'") {
					mutations = append(mutations, cmd)
					if mode == "clear" && strings.Contains(cmd, "'67'") {
						source = strings.ReplaceAll(source, `text="old"`, `text=""`)
					}
					if strings.Contains(cmd, "'text'") && mode != "failed_readback" {
						source = strings.ReplaceAll(source, `text="old"`, `text="new"`)
					}
				}
				return nil, nil
			}
			text := "new"
			if mode == "clear" {
				text = ""
			}
			if mode == "unicode" {
				text = "new 💬"
			}
			_, err := d.Action(context.Background(), Session{ID: "s", Device: Device{Platform: "android", Driver: "adb", UDID: "phone"}}, "text", map[string]any{"replace": true, "text": text, "expected_package": "com.example", "expected_resource_id": "bio", "expected_current": "old"})
			if (err == nil) != (mode == "good" || mode == "clear") {
				t.Fatal(mode, err)
			}
			if mode == "clear" {
				if len(mutations) != 2 {
					t.Fatal(mutations)
				}
			} else if mode == "good" || mode == "failed_readback" {
				if len(mutations) != 3 {
					t.Fatal(mutations)
				}
			} else if len(mutations) != 0 {
				t.Fatal("input dispatched without matching state", mutations)
			}
		})
	}
}

func TestAndroidAnonymousFocusedFieldRequiresExactIdentity(t *testing.T) {
	source := `<hierarchy><node package="com.example"><node package="com.example" resource-id="" class="android.widget.EditText" text="old" focused="true" enabled="true" visible-to-user="true" bounds="[10,20][100,200]"/></node></hierarchy>`
	for _, mode := range []string{"good", "wrong_bounds", "wrong_class", "wrong_value"} {
		kind, bounds, value := "android.widget.EditText", "[10,20][100,200]", "old"
		if mode == "wrong_bounds" {
			bounds = "[0,0][100,200]"
		}
		if mode == "wrong_class" {
			kind = "other"
		}
		if mode == "wrong_value" {
			value = "different"
		}
		err := matchAndroidFocusedFieldDetails(source, "com.example", "", value, kind, bounds)
		if (err == nil) != (mode == "good") {
			t.Fatal(mode, err)
		}
	}
}
