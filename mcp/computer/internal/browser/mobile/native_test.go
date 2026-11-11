package mobile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestNativeScreenChecksBeforeTapping(t *testing.T) {
	const app = `<XCUIElementTypeApplication name="Instagram" width="400" height="800">`
	const end = `</XCUIElementTypeApplication>`
	const picker = `<XCUIElementTypeStaticText label="New post" visible="true"/>`
	const next = `<XCUIElementTypeButton label="Next" enabled="true" visible="true" x="100" y="100" width="200" height="200"/>`
	const share = `<XCUIElementTypeButton label="Share" enabled="true" visible="true"/>`
	const label = `<XCUIElementTypeOther label="Next" enabled="true" visible="true"/>`
	nested := strings.TrimSuffix(next, "/>") + ">" + label + "</XCUIElementTypeButton>"
	for _, tc := range []struct {
		name, source string
		wantTap      bool
		wantCenter   bool
	}{
		{"exact picker control", app + picker + next + end, true, false},
		{"button with matching label child", app + picker + nested + end, true, true},
		{"separate label is ambiguous", app + picker + nested + label + end, false, false},
		{"offscreen button bounds", app + picker + strings.Replace(nested, `y="100"`, `y="700"`, 1) + end, false, false},
		{"already at share screen", app + picker + share + end, false, false},
		{"same button exposed twice", app + picker + next + next + end, true, true},
		{"different buttons with the same label", app + picker + next + strings.Replace(next, `x="100"`, `x="200"`, 1) + end, false, false},
		{"missing screen marker", app + next + end, false, false},
		{"wrong app", strings.Replace(app, "Instagram", "Other", 1) + picker + next + end, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taps := 0
			c := &Computer{
				lease: lease{ID: "native-test", Device: device{Platform: "ios", Driver: "xctest", Capabilities: []string{"source", "elements"}}, ExpiresAt: time.Now().Add(time.Hour).Unix()},
				call: func(_ context.Context, tool string, args map[string]any, out any) error {
					switch tool {
					case "mobile_observe":
						*out.(*sdk.MCPResult) = sdk.MCPResult{StructuredContent: map[string]any{"source": tc.source}}
					case "mobile_action":
						if tc.wantCenter {
							if args["action"] != "tap" || args["x"] != 0.5 || args["y"] != 0.25 {
								t.Fatalf("did not tap verified button center: %v", args)
							}
						} else if args["action"] != "tap_element" || args["using"] != "accessibility id" || args["value"] != "Next" {
							t.Fatalf("unexpected native action %v", args)
						}
						taps++
					default:
						t.Fatalf("unexpected tool %s", tool)
					}
					return nil
				},
			}
			err := c.NativeScreen("native_tap", "Instagram", []string{"New post"}, "Next", "")
			if (err == nil) != tc.wantTap || (taps == 1) != tc.wantTap {
				t.Fatalf("tap count %d, error %v", taps, err)
			}
		})
	}
}

func TestNativeTapBadgedUsesExactBaseAndCurrentLabel(t *testing.T) {
	const app = `<XCUIElementTypeApplication name="Instagram">`
	const sheet = `<XCUIElementTypeButton name="bottom_sheet_background" visible="true"/>`
	const base = "INSTAGRAM profile, apteva.test"
	const longer = base + "_other, 4 notifications"
	button := func(label string) string {
		return `<XCUIElementTypeButton label="` + label + `" visible="true" enabled="true"/>`
	}
	for _, tc := range []struct {
		name, rows, wantTap string
	}{
		{"no badge", button(base) + button(longer), base},
		{"notification badge", button(base + ", 1 notification"), base + ", 1 notification"},
		{"changed badge", button(base + ", 1 chat and 3 more"), base + ", 1 chat and 3 more"},
		{"unfamiliar badge", button(base + ", New activity"), base + ", New activity"},
		{"longer handle", button(longer), ""},
		{"duplicate base", button(base) + button(base+", 1 notification"), ""},
		{"disabled row", strings.Replace(button(base), `enabled="true"`, `enabled="false"`, 1), ""},
		{"hidden row", strings.Replace(button(base), `visible="true"`, `visible="false"`, 1), ""},
		{"non-button", strings.Replace(button(base), "XCUIElementTypeButton", "XCUIElementTypeOther", 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tapped string
			c := &Computer{
				lease: lease{ID: "native-test", Device: device{Platform: "ios", Driver: "xctest", Capabilities: []string{"source", "elements"}}, ExpiresAt: time.Now().Add(time.Hour).Unix()},
				call: func(_ context.Context, tool string, args map[string]any, out any) error {
					switch tool {
					case "mobile_observe":
						*out.(*sdk.MCPResult) = sdk.MCPResult{StructuredContent: map[string]any{"source": app + sheet + tc.rows + "</XCUIElementTypeApplication>"}}
					case "mobile_action":
						if args["action"] != "tap_element" || args["using"] != "accessibility id" {
							t.Fatalf("expected an exact element tap: %v", args)
						}
						tapped, _ = args["value"].(string)
					default:
						t.Fatalf("unexpected tool %s", tool)
					}
					return nil
				},
			}
			err := c.NativeScreen("native_tap_badged", "Instagram", []string{"bottom_sheet_background"}, base, "")
			if (err == nil) != (tc.wantTap != "") || tapped != tc.wantTap {
				t.Fatalf("tap %q, want %q, error %v", tapped, tc.wantTap, err)
			}
		})
	}
}

func TestNativeSecureTypingGuardsThePasswordField(t *testing.T) {
	const secret = "fixture-secret"
	const app = `<XCUIElementTypeApplication name="Instagram"><XCUIElementTypeButton name="Show password" visible="true"/>`
	const end = `</XCUIElementTypeApplication>`
	const empty = app + `<XCUIElementTypeSecureTextField label="Password" value="Password" visible="true" enabled="true"/>` + end
	const filled = app + `<XCUIElementTypeSecureTextField label="Password" value="••••••" visible="true" enabled="true"/>` + end
	plain := strings.Replace(empty, "SecureTextField", "TextField", 1)
	for _, tc := range []struct {
		name       string
		sources    []string
		typeError  error
		actions    []string
		wantPassed bool
	}{
		{"verified secure field", []string{empty, empty, filled}, nil, []string{"tap_element", "text"}, true},
		{"ordinary field", []string{plain}, nil, nil, false},
		{"field changed when focused", []string{empty, plain}, nil, []string{"tap_element"}, false},
		{"field did not receive text", []string{empty, empty, empty}, nil, []string{"tap_element", "text"}, false},
		{"input error is sanitized", []string{empty, empty}, errors.New(secret), []string{"tap_element", "text"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actions []string
			sourceIndex := 0
			c := &Computer{
				lease: lease{ID: "native-test", Device: device{Platform: "ios", Driver: "xctest", Capabilities: []string{"source", "elements"}}, ExpiresAt: time.Now().Add(time.Hour).Unix()},
				call: func(_ context.Context, tool string, args map[string]any, out any) error {
					switch tool {
					case "mobile_observe":
						*out.(*sdk.MCPResult) = sdk.MCPResult{StructuredContent: map[string]any{"source": tc.sources[sourceIndex]}}
						sourceIndex++
					case "mobile_action":
						action, _ := args["action"].(string)
						actions = append(actions, action)
						if action == "text" {
							return tc.typeError
						}
					default:
						t.Fatalf("unexpected tool %s", tool)
					}
					return nil
				},
			}
			err := c.NativeScreen("native_type_secure", "Instagram", []string{"Show password"}, "Password", secret)
			if (err == nil) != tc.wantPassed || !reflect.DeepEqual(actions, tc.actions) || (err != nil && strings.Contains(err.Error(), secret)) {
				t.Fatalf("unexpected secure typing result for %v", actions)
			}
		})
	}
}

func TestNativeReplacementChecksTheFinalFieldValue(t *testing.T) {
	const app = `<XCUIElementTypeApplication name="Instagram"><XCUIElementTypeOther name="Create a username" visible="true"/>`
	const field = `<XCUIElementTypeTextField name="Username" value="suggested" visible="true" enabled="true"/>`
	const end = `</XCUIElementTypeApplication>`
	for _, result := range []string{"apteva.test", "suggestedapteva.test"} {
		t.Run(result, func(t *testing.T) {
			var typed string
			c := &Computer{
				lease: lease{ID: "native-test", Device: device{Platform: "ios", Driver: "xctest", Capabilities: []string{"source", "elements"}}, ExpiresAt: time.Now().Add(time.Hour).Unix()},
				call: func(_ context.Context, tool string, args map[string]any, out any) error {
					switch tool {
					case "mobile_observe":
						xml := app + field + end
						if typed != "" {
							xml = strings.Replace(xml, "suggested", result, 1)
						}
						*out.(*sdk.MCPResult) = sdk.MCPResult{StructuredContent: map[string]any{"source": xml}}
					case "mobile_action":
						if args["action"] == "text" {
							typed, _ = args["text"].(string)
						}
					default:
						t.Fatalf("unexpected tool %s", tool)
					}
					return nil
				},
			}
			err := c.NativeScreen("native_replace", "Instagram", []string{"Create a username"}, "Username", "apteva.test")
			if typed != strings.Repeat("\b", len("suggested"))+"apteva.test" || (err == nil) != (result == "apteva.test") {
				t.Fatalf("unexpected verified replacement outcome: %v", err)
			}
		})
	}
}
