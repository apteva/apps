package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

func TestNormalizeBrowserSessionArgsDropsSynthesizedDefaults(t *testing.T) {
	args := map[string]any{
		"action": " open ", "backend": " local ", "url": " https://example.com ",
		"context_id": "", "context_name": "", "provider_context_id": "",
		"auto_create_context": false, "persist": false, "timeout": float64(0),
		"proxy": false, "proxy_mode": "auto", "proxy_profile": "", "proxy_country": "", "proxy_sticky": "",
		"viewport": map[string]any{"width": float64(0), "height": float64(0)},
		"environment": map[string]any{
			"user_agent": "", "locale": "", "languages": []any{}, "timezone": "",
			"geolocation": map[string]any{}, "device_scale_factor": float64(0),
			"mobile": false, "touch": false, "max_touch_points": float64(1),
		},
	}
	got := normalizeBrowserSessionArgs(args)
	if got["action"] != "open" || got["backend"] != "local" || got["url"] != "https://example.com" {
		t.Fatalf("trimmed args = %#v", got)
	}
	for _, key := range []string{"context_id", "context_name", "provider_context_id", "timeout", "viewport", "environment", "proxy_profile", "proxy_country", "proxy_sticky"} {
		if _, exists := got[key]; exists {
			t.Fatalf("default-filled %q was not removed: %#v", key, got)
		}
	}
	if proxy, ok := got["proxy"].(bool); !ok || proxy {
		t.Fatalf("legacy proxy=false must survive normalization for backward compatibility: %#v", got)
	}
	if got["persist"] != false || got["auto_create_context"] != false {
		t.Fatalf("meaningful lifecycle booleans changed: %#v", got)
	}
}

func TestNormalizeSessionEnvironmentPreservesIntentionalZeroCoordinates(t *testing.T) {
	got := normalizeBrowserSessionArgs(map[string]any{"environment": map[string]any{
		"locale": " en-GB ", "mobile": false,
		"geolocation": map[string]any{"latitude": float64(0), "longitude": float64(0), "permission": ""},
	}})
	environment, ok := got["environment"].(map[string]any)
	if !ok || environment["locale"] != "en-GB" || environment["mobile"] != false {
		t.Fatalf("meaningful environment was not preserved: %#v", got)
	}
	geo, ok := environment["geolocation"].(map[string]any)
	if !ok || geo["latitude"] != float64(0) || geo["longitude"] != float64(0) {
		t.Fatalf("valid zero coordinates were discarded: %#v", environment)
	}
}

func TestBrowserSessionAcceptsOverSpecifiedDefaultArguments(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })
	fake := &fakeComp{display: backends.DisplaySize{Width: 1600, Height: 800}, url: "https://example.com/"}
	var config backends.Config
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		config = cfg
		return fake, nil
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action": "open", "backend": "local", "url": "https://example.com",
		"session_id": "", "tab_id": "", "context_id": "", "context_name": "", "provider_context_id": "",
		"auto_create_context": false, "persist": false, "timeout": float64(0), "presentation_mode": "",
		"proxy": false, "proxy_mode": "auto", "proxy_profile": "", "proxy_country": "", "proxy_sticky": "session",
		"viewport": map[string]any{"width": float64(0), "height": float64(0)},
		"environment": map[string]any{
			"user_agent": "", "locale": "", "languages": []any{}, "timezone": "",
			"geolocation": map[string]any{}, "device_scale_factor": float64(0),
			"mobile": false, "touch": false, "max_touch_points": float64(1),
		},
	})
	if err != nil {
		t.Fatalf("over-specified default session call was rejected: %v", err)
	}
	result := out.(map[string]any)
	if result["session_id"] == "" || config.Width != 0 || config.Height != 0 {
		t.Fatalf("normalized open result=%#v config=%+v", result, config)
	}
	proxy, ok := result["proxy"].(SessionProxyState)
	if !ok || proxy.Mode != "auto" || fake.openProxy != nil {
		t.Fatalf("proxy_mode did not win over proxy=false: result=%#v openProxy=%v", result["proxy"], fake.openProxy)
	}
	if !fake.openEnvironment.IsEmpty() {
		t.Fatalf("empty environment became an override: %+v", fake.openEnvironment)
	}
}
