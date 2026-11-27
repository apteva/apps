package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const fixtureSecretValue = "fixture-secret-value-9f3"

func secretConfig(entries map[string]map[string]mobileSecret, literal string) func(string) string {
	raw, _ := json.Marshal(entries)
	return func(k string) string {
		switch k {
		case "mobile_secrets":
			return string(raw)
		case "allow_literal_secure_text":
			return literal
		}
		return ""
	}
}

func TestResolveSecureText(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	future, past := now.Add(time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339)
	entries := map[string]map[string]mobileSecret{
		"p": {
			"bank":     {Value: fixtureSecretValue, Package: "com.example.bank", ResourceID: "com.example.bank:id/password", DeviceID: "R5G", ExpiresAt: future},
			"loose":    {Value: fixtureSecretValue, Package: "com.example.bank", ExpiresAt: future},
			"expired":  {Value: fixtureSecretValue, Package: "com.example.bank", ExpiresAt: past},
			"no-expry": {Value: fixtureSecretValue, Package: "com.example.bank"},
			"no-pkg":   {Value: fixtureSecretValue, ExpiresAt: future},
		},
		"other": {"theirs": {Value: fixtureSecretValue, Package: "com.example.bank", ExpiresAt: future}},
	}
	cfg := secretConfig(entries, "")
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"action": "secure_text", "session_id": "s", "expected_bounds": "[0,0][1,1]"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		args map[string]any
		get  func(string) string
		code string
	}{
		{"ref", base(map[string]any{"secret_ref": "bank"}), cfg, ""},
		{"ref with matching guards", base(map[string]any{"secret_ref": "bank", "expected_package": "com.example.bank", "expected_resource_id": "com.example.bank:id/password"}), cfg, ""},
		{"literal allowed by default", base(map[string]any{"password": "typed"}), cfg, ""},
		{"neither", base(nil), cfg, "invalid_argument"},
		{"both", base(map[string]any{"secret_ref": "bank", "password": "typed"}), cfg, "invalid_argument"},
		{"literal disabled", base(map[string]any{"password": "typed"}), secretConfig(entries, "false"), "forbidden"},
		{"unknown ref", base(map[string]any{"secret_ref": "nope"}), cfg, "secret_unavailable"},
		{"other project's ref", base(map[string]any{"secret_ref": "theirs"}), cfg, "secret_unavailable"},
		{"expired", base(map[string]any{"secret_ref": "expired"}), cfg, "secret_unavailable"},
		{"no expiry", base(map[string]any{"secret_ref": "no-expry"}), cfg, "secret_unavailable"},
		{"no package binding", base(map[string]any{"secret_ref": "no-pkg"}), cfg, "secret_unavailable"},
		{"bad config", base(map[string]any{"secret_ref": "bank"}), func(string) string { return "{" }, "secret_unavailable"},
		{"wrong package", base(map[string]any{"secret_ref": "bank", "expected_package": "com.evil"}), cfg, "forbidden"},
		{"wrong field", base(map[string]any{"secret_ref": "bank", "expected_resource_id": "com.example.bank:id/username"}), cfg, "forbidden"},
		{"non-string ref", base(map[string]any{"secret_ref": 7}), cfg, "invalid_argument"},
		{"ref on another action", map[string]any{"action": "text", "text": "x", "secret_ref": "bank"}, cfg, "invalid_argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, _ := json.Marshal(c.args)
			out, err := resolveSecureText(c.args, "p", c.get, now)
			if after, _ := json.Marshal(c.args); string(after) != string(before) {
				t.Fatal("caller's arguments were modified")
			}
			if c.code != "" {
				if errorCode(err) != c.code {
					t.Fatalf("code=%q err=%v", errorCode(err), err)
				}
				if strings.Contains(err.Error(), fixtureSecretValue) {
					t.Fatal("value in error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := c.args["secret_ref"]; ok {
				if out["password"] != fixtureSecretValue || out["expected_package"] != "com.example.bank" || out["secret_ref"] != nil {
					t.Fatalf("not resolved: %v", out["expected_package"])
				}
				if c.args["secret_ref"] == "bank" && (out["expected_resource_id"] != "com.example.bank:id/password" || out[secretDeviceArg] != "R5G") {
					t.Fatal("bindings not applied as guards")
				}
				if b, _ := json.Marshal(summarizeArgs(out)); strings.Contains(string(b), fixtureSecretValue) {
					t.Fatal("value in action event summary")
				}
			}
		})
	}
}

func TestResolveSecureTextStripsCallerDeviceBinding(t *testing.T) {
	out, err := resolveSecureText(map[string]any{"action": "tap", secretDeviceArg: "x"}, "p", func(string) string { return "" }, time.Now())
	if err != nil || out[secretDeviceArg] != nil {
		t.Fatalf("internal argument accepted from caller: %v", err)
	}
}

func TestSecureTextDeviceBinding(t *testing.T) {
	for _, tc := range []struct {
		device string
		ok     bool
	}{{"", true}, {"android:phone", true}, {"phone", true}, {"android:other", false}} {
		t.Run(tc.device, func(t *testing.T) {
			e, b := fixture(t)
			s := openFixture(t, e, "p")
			args := map[string]any{"session_id": s.ID, "action": "secure_text", "password": fixtureSecretValue}
			if tc.device != "" {
				args[secretDeviceArg] = tc.device
			}
			_, err := e.Execute(context.Background(), "p", "mobile_action", args)
			b.mu.Lock()
			calls := b.actions
			b.mu.Unlock()
			if tc.ok != (calls == 1) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !tc.ok && (errorCode(err) != "forbidden" || strings.Contains(err.Error(), fixtureSecretValue)) {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}
