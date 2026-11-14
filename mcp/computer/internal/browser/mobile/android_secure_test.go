package mobile

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/computer/internal/browser/api"
	"strings"
	"testing"
	"time"
)

func TestAndroidSecureInputGuardsAndReceipt(t *testing.T) {
	const password = "private-test-password"
	for _, mode := range []string{"valid", "wrong_user", "regular_field", "wrong_app", "missing_capability", "missing_receipt", "rejected_receipt", "input_error"} {
		t.Run(mode, func(t *testing.T) {
			source := strings.Replace(androidFixture("test-account"), `text="Old bio"`, `password="true" text=""`, 1)
			if mode == "wrong_user" {
				source = androidFixture("main-account")
			}
			if mode == "regular_field" {
				source = androidFixture("test-account")
			}
			if mode == "wrong_app" {
				source = strings.ReplaceAll(source, "com.example", "com.other")
			}
			inputs := 0
			c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
				if tool == "mobile_observe" {
					if args["image"] != false {
						t.Fatal("private source requested image")
					}
					b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": source}})
					return json.Unmarshal(b, out)
				}
				if tool == "mobile_action" {
					inputs++
					if args["action"] != "secure_text" || args["password"] != password || args["text"] != nil {
						t.Fatal("unsafe secure action")
					}
					if mode == "input_error" {
						return errors.New(password)
					}
					receipt := map[string]any{"secure_input": true, "verified": true}
					if mode == "missing_receipt" {
						receipt = map[string]any{}
					}
					if mode == "rejected_receipt" {
						receipt["verified"] = false
					}
					b, _ := json.Marshal(receipt)
					return json.Unmarshal(b, out)
				}
				return nil
			})
			c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: []string{"source", "source_only", "secure_text"}}}
			c.display = api.DisplaySize{Width: 100, Height: 200}
			c.ttl = 900
			if mode == "missing_capability" {
				c.lease.Device.Capabilities = []string{"source"}
			}
			_, err := c.NativeGuarded("native_type_secure", "com.example", []string{"header"}, "bio", password, map[string]string{"header": "test-account"}, 0)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("unexpected outcome %v", err)
			}
			if err != nil && strings.Contains(err.Error(), password) {
				t.Fatal("secret escaped error")
			}
			if (mode == "wrong_user" || mode == "regular_field" || mode == "wrong_app" || mode == "missing_capability") && inputs != 0 {
				t.Fatal("input before guards")
			}
		})
	}
}
