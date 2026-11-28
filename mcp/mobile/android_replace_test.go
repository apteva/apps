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

func TestReplacementVerificationAllowsResizedField(t *testing.T) {
	const old = "[40,1800][1040,1900]"
	dump := func(fields ...string) string {
		s := `<hierarchy><node package="com.example" bounds="[0,0][1080,2340]">`
		for _, f := range fields {
			s += f
		}
		return s + `</node></hierarchy>`
	}
	field := func(id, bounds string) string {
		return `<node package="com.example" resource-id="` + id + `" class="android.widget.EditText" text="long reply" focused="true" enabled="true" visible-to-user="true" bounds="` + bounds + `"/>`
	}
	for _, c := range []struct {
		name, id, source string
		ok               bool
	}{
		{"unchanged", "", dump(field("", old)), true},
		{"grows down, top fixed", "", dump(field("", "[40,1800][1040,2050]")), true},
		{"grows up above the keyboard, bottom fixed", "", dump(field("", "[40,1600][1040,1900]")), true},
		{"centered dialog grows both ways", "", dump(field("", "[40,1750][1040,1950]")), true},
		{"edges within tolerance", "", dump(field("", "[42,1800][1038,2000]")), true},
		{"focus moved to a field elsewhere", "", dump(field("", "[40,600][1040,700]")), false},
		{"different left edge", "", dump(field("", "[200,1800][1040,2000]")), false},
		{"different right edge", "", dump(field("", "[40,1800][800,2000]")), false},
		{"zero height", "", dump(field("", "[40,1850][1040,1850]")), false},
		{"taller than the screen", "", dump(field("", "[40,-100][1040,2400]")), false},
		{"two focused fields", "", dump(field("", "[40,1800][1040,2000]"), field("", "[40,1700][1040,1900]")), false},
		{"resource ID, bounds changed", "reply", dump(field("reply", "[0,100][500,300]")), true},
		{"resource ID, wrong ID", "reply", dump(field("other", old)), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := matchFocusedField(c.source, "com.example", c.id, "long reply", "android.widget.EditText", old, true)
			if (err == nil) != c.ok {
				t.Fatalf("ok=%v err=%v", c.ok, err)
			}
			if err == nil && (got == "" || !strings.Contains(c.source, `bounds="`+got+`"`)) {
				t.Fatalf("returned bounds %q", got)
			}
		})
	}
}

func TestReplacementPrecheckStillNeedsExactBounds(t *testing.T) {
	source := `<hierarchy><node package="com.example" bounds="[0,0][1080,2340]"><node package="com.example" resource-id="" class="android.widget.EditText" text="old" focused="true" enabled="true" visible-to-user="true" bounds="[40,1800][1040,1902]"/></node></hierarchy>`
	if err := matchAndroidFocusedFieldDetails(source, "com.example", "", "old", "android.widget.EditText", "[40,1800][1040,1900]"); err == nil {
		t.Fatal("pre-check accepted a different box")
	}
	if err := matchAndroidFocusedFieldDetails(strings.Replace(source, `resource-id=""`, `resource-id="reply"`, 1), "com.example", "reply", "old", "android.widget.EditText", "[40,1800][1040,1900]"); err == nil {
		t.Fatal("pre-check with a resource ID accepted a different box")
	}
	if err := matchAndroidFocusedFieldDetails(strings.Replace(source, `resource-id=""`, `resource-id="reply"`, 1), "com.example", "reply", "old", "android.widget.EditText", "[40,1800][1040,1902]"); err != nil {
		t.Fatal("pre-check rejected the exact box", err)
	}
}

func TestReplaceReturnsNewBoundsAfterFieldGrows(t *testing.T) {
	source := `<hierarchy><node package="com.example" bounds="[0,0][1080,2340]"><node package="com.example" resource-id="" class="android.widget.EditText" text="old" focused="true" enabled="true" visible-to-user="true" bounds="[40,1800][1040,1900]"/></node></hierarchy>`
	d := newDrivers(runnerConfig{})
	d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		cmd := args[len(args)-1]
		if len(args) > 2 && args[2] == "exec-out" {
			return testPNG(), nil
		}
		if strings.Contains(cmd, "'cat'") {
			return []byte(source), nil
		}
		if strings.Contains(cmd, "'input'") && strings.Contains(cmd, "'text'") {
			// The comment box wraps and grows upward above the keyboard.
			source = strings.NewReplacer(`text="old"`, `text="a much longer reply"`, "[40,1800][1040,1900]", "[40,1640][1040,1900]").Replace(source)
		}
		return nil, nil
	}
	out, err := d.Action(context.Background(), Session{ID: "s", Device: Device{Platform: "android", Driver: "adb", UDID: "phone"}}, "text", map[string]any{"replace": true, "text": "a much longer reply", "expected_package": "com.example", "expected_class": "android.widget.EditText", "expected_bounds": "[40,1800][1040,1900]", "expected_current": "old"})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); m["verified"] != true || m["bounds"] != "[40,1640][1040,1900]" {
		t.Fatalf("result %v", out)
	}
}
