package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func androidFixture(handle string) string {
	return `<hierarchy rotation="0"><node package="com.example" class="android.widget.FrameLayout" enabled="true" bounds="[0,0][100,200]">` +
		fmt.Sprintf(`<node package="com.example" resource-id="header" class="android.widget.TextView" text="%s" enabled="true" bounds="[0,0][100,20]"/>`, handle) +
		`<node package="com.example" class="android.widget.Button" content-desc="Edit profile" clickable="true" enabled="true" bounds="[0,20][100,50]"><node package="com.example" text="Edit profile" enabled="true" bounds="[10,25][90,45]"/></node>` +
		`<node package="com.example" resource-id="bio" class="android.widget.EditText" text="Old bio" focused="true" clickable="true" enabled="true" bounds="[0,60][100,100]"/>` +
		`<node package="com.example" text="test-account" enabled="true" bounds="[0,110][100,150]"/></node></hierarchy>`
}

func TestAndroidGuardsRejectWrongStateBeforeInput(t *testing.T) {
	for _, mode := range []string{"good", "wrong_account", "wrong_app", "ambiguous", "disabled", "password", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			source := androidFixture("test-account")
			switch mode {
			case "wrong_account":
				source = androidFixture("main-account")
			case "wrong_app":
				source = strings.ReplaceAll(source, "com.example", "com.other")
			case "ambiguous":
				source = strings.Replace(source, `</hierarchy>`, `<node package="com.example" resource-id="header" text="test-account" enabled="true" bounds="[0,0][100,20]"/></hierarchy>`, 1)
			case "disabled":
				source = strings.ReplaceAll(source, `enabled="true"`, `enabled="false"`)
			case "password":
				source = strings.Replace(source, `resource-id="header"`, `password="true" resource-id="header"`, 1)
			case "malformed":
				source = source[:len(source)-15]
			}
			inputs := 0
			c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
				if tool == "mobile_observe" {
					b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": source}})
					return json.Unmarshal(b, out)
				}
				if tool == "mobile_action" {
					inputs++
				}
				return nil
			})
			c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: []string{"source", "replace_text"}}}
			c.display = api.DisplaySize{Width: 100, Height: 200}
			c.ttl = 900
			_, err := c.NativeGuarded("native_tap", "com.example", []string{"header", "Edit profile"}, "Edit profile", "", map[string]string{"header": "test-account"}, 0)
			if (err == nil) != (mode == "good") || (inputs > 0) != (mode == "good") {
				t.Fatalf("err=%v inputs=%d", err, inputs)
			}
		})
	}
}

func TestAndroidReadAndReplacementVerification(t *testing.T) {
	for _, mode := range []string{"read", "replace", "clear", "wrong_focus", "unverified", "unicode"} {
		t.Run(mode, func(t *testing.T) {
			source := androidFixture("test-account")
			inputs := 0
			c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
				if tool == "mobile_observe" {
					b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": source}})
					return json.Unmarshal(b, out)
				}
				if tool == "mobile_action" {
					inputs++
					if args["action"] == "tap" && mode == "wrong_focus" {
						source = strings.ReplaceAll(source, `focused="true"`, `focused="false"`)
					}
					if args["action"] == "text" {
						if args["replace"] != true || args["expected_resource_id"] != "bio" || args["expected_current"] != "Old bio" {
							t.Fatal(args)
						}
						if mode != "unverified" {
							replacement := "New bio"
							if mode == "clear" {
								replacement = ""
							}
							source = strings.ReplaceAll(source, "Old bio", replacement)
						}
					}
				}
				return nil
			})
			c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: []string{"source", "replace_text"}}}
			c.display = api.DisplaySize{Width: 100, Height: 200}
			c.ttl = 900
			action, text := "native_replace", "New bio"
			if mode == "read" {
				action, text = "native_read", ""
			}
			if mode == "clear" {
				action, text = "native_clear", ""
			}
			if mode == "unicode" {
				text = "bio 💬"
			}
			value, err := c.NativeGuarded(action, "com.example", []string{"header", "bio"}, "bio", text, map[string]string{"header": "test-account"}, 0)
			if (err == nil) != (mode == "read" || mode == "replace" || mode == "clear") {
				t.Fatal(err)
			}
			if mode == "read" && (value != "Old bio" || inputs != 0) {
				t.Fatal(value, inputs)
			}
			if mode == "wrong_focus" && inputs != 1 {
				t.Fatal("sent text after focus changed")
			}
			if mode == "unicode" && inputs != 0 {
				t.Fatal("unsupported bytes changed the field")
			}
		})
	}
}

func TestAndroidEnabledAccountInsideDisabledLayout(t *testing.T) {
	for _, mode := range []string{"enabled_row", "disabled_row", "hidden_layout"} {
		t.Run(mode, func(t *testing.T) {
			visible, enabled := "true", "true"
			if mode == "disabled_row" {
				enabled = "false"
			}
			if mode == "hidden_layout" {
				visible = "false"
			}
			source := fmt.Sprintf(`<hierarchy><node package="com.example" enabled="false" visible-to-user="%s" bounds="[0,0][100,200]"><node package="com.example" enabled="%s" clickable="true" content-desc="test-account, 3 notifications" bounds="[0,20][100,80]"><node package="com.example" enabled="true" text="test-account" bounds="[10,30][90,60]"/></node></node></hierarchy>`, visible, enabled)
			nodes, err := androidTree(source, "com.example", 100, 200)
			if err != nil {
				t.Fatal(err)
			}
			_, err = matchAndroid(nodes, []string{"test-account"}, "test-account", true, true, nil)
			if (err == nil) != (mode == "enabled_row") {
				t.Fatalf("unexpected match: %v", err)
			}
		})
	}
}

func TestAndroidDirectChildPathScopesRepeatedFields(t *testing.T) {
	source := `<hierarchy><node package="com.example" enabled="true" bounds="[0,0][100,200]"><node package="com.example" resource-id="bio" enabled="true" bounds="[0,0][100,100]"><node package="com.example" resource-id="form" class="android.widget.EditText" enabled="true" bounds="[0,0][100,100]"><node package="com.example" class="android.widget.EditText" text="bio value" enabled="true" clickable="true" bounds="[0,30][100,100]"/></node></node><node package="com.example" class="android.widget.EditText" text="other field" enabled="true" bounds="[0,110][100,190]"/></node></hierarchy>`
	nodes, err := androidTree(source, "com.example", 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	n, err := matchAndroid(nodes, nil, "bio > form > android.widget.EditText", false, true, nil)
	if err != nil || n.value != "bio value" {
		t.Fatal(n, err)
	}
	for _, path := range []string{"wrong > form > android.widget.EditText", "bio > form > android.widget.Button"} {
		if _, err := matchAndroid(nodes, nil, path, false, false, nil); err == nil {
			t.Fatal("accepted wrong path", path)
		}
	}
}

func TestAndroidDuplicateComposeButtonMustBeNestedAndIdentical(t *testing.T) {
	for _, mode := range []string{"same_button", "different_bounds", "separate_buttons"} {
		source := `<hierarchy><node package="com.example" enabled="true" bounds="[0,0][100,200]"><node package="com.example" content-desc="Back" clickable="true" enabled="true" bounds="[0,0][100,50]"><node package="com.example" content-desc="Back" clickable="true" enabled="true" bounds="[0,0][100,50]"/></node></node></hierarchy>`
		if mode == "different_bounds" {
			source = strings.Replace(source, `bounds="[0,0][100,50]"/>`, `bounds="[10,0][100,50]"/>`, 1)
		}
		if mode == "separate_buttons" {
			source = strings.Replace(source, `/></node></node>`, `/></node><node package="com.example" content-desc="Back" clickable="true" enabled="true" bounds="[0,0][100,50]"/></node>`, 1)
		}
		nodes, err := androidTree(source, "com.example", 100, 200)
		if err != nil {
			t.Fatal(err)
		}
		_, err = matchAndroid(nodes, nil, "Back", false, true, nil)
		if (err == nil) != (mode == "same_button") {
			t.Fatal(mode, err)
		}
	}
}

func TestAndroidReplacementAdvancesOnlyEditedFieldGuard(t *testing.T) {
	source := androidFixture("test-account")
	c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
		if tool == "mobile_observe" {
			b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": source}})
			return json.Unmarshal(b, out)
		}
		if tool == "mobile_action" && args["action"] == "text" {
			source = strings.Replace(source, "Old bio", "New bio", 1)
		}
		return nil
	})
	c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: []string{"source", "replace_text"}}}
	c.display = api.DisplaySize{Width: 100, Height: 200}
	c.ttl = 900
	guards := map[string]string{"header": "test-account", "bio": "Old bio"}
	if _, err := c.NativeGuarded("native_replace", "com.example", []string{"header"}, "bio", "New bio", guards, 0); err != nil {
		t.Fatal(err)
	}
	if guards["bio"] != "Old bio" {
		t.Fatal("caller guards mutated")
	}
}

func TestAndroidSecureSelectorRequiresOnePasswordField(t *testing.T) {
	source := strings.Replace(androidFixture("test-account"), `text="Old bio"`, `password="true" text=""`, 1)
	nodes, err := androidTree(source, "com.example", 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	field, err := matchAndroid(nodes, []string{"header"}, "android.widget.EditText[password=true]", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes = append(nodes, field)
	if _, err = matchAndroid(nodes, []string{"header"}, "android.widget.EditText[password=true]", false, false, nil); err == nil {
		t.Fatal("ambiguous password target accepted")
	}
}
