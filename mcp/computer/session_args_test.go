package main

import (
	"slices"
	"strings"
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
	if _, ok := got["proxy"]; ok {
		t.Fatalf("legacy proxy must be ignored when proxy_mode is authoritative: %#v", got)
	}
	if got["persist"] != false {
		t.Fatalf("meaningful lifecycle booleans changed: %#v", got)
	}
	if _, ok := got["auto_create_context"]; ok {
		t.Fatalf("default auto_create_context=false was not removed: %#v", got)
	}
}

func TestNormalizeSessionEnvironmentPreservesIntentionalZeroCoordinates(t *testing.T) {
	got := normalizeBrowserSessionArgs(map[string]any{"environment": map[string]any{
		"locale": " en-GB ", "mobile": false,
		"geolocation": map[string]any{"latitude": float64(0), "longitude": float64(0), "permission": ""},
	}})
	environment, ok := got["environment"].(map[string]any)
	if !ok || environment["locale"] != "en-GB" {
		t.Fatalf("meaningful environment was not preserved: %#v", got)
	}
	if _, exists := environment["mobile"]; exists {
		t.Fatalf("default mobile=false was not removed: %#v", environment)
	}
	geo, ok := environment["geolocation"].(map[string]any)
	if !ok || geo["latitude"] != float64(0) || geo["longitude"] != float64(0) {
		t.Fatalf("valid zero coordinates were discarded: %#v", environment)
	}
}

func TestNormalizeSavedContextOpenDropsOnlyGeneratedEnvironmentDefaults(t *testing.T) {
	tests := map[string]any{
		"null":  nil,
		"empty": map[string]any{},
		"generated schema values": map[string]any{
			"user_agent": "", "locale": "", "languages": []any{}, "timezone": "UTC",
			"geolocation":         map[string]any{"latitude": 0.0, "longitude": 0.0, "accuracy": 0.0, "permission": "prompt"},
			"device_scale_factor": 0.5, "mobile": false, "touch": false, "max_touch_points": 1.0,
		},
	}
	for name, environment := range tests {
		t.Run(name, func(t *testing.T) {
			got, diagnostics := normalizeBrowserSessionArgsWithDiagnostics(map[string]any{
				"action": "open", "context_name": "Alexa Patreon", "url": "https://www.patreon.com/c/alexaentranced",
				"environment": environment,
			})
			if _, exists := got["environment"]; exists {
				t.Fatalf("generated environment was not removed from saved-context open: %#v", got)
			}
			if got["context_name"] != "Alexa Patreon" || got["url"] != "https://www.patreon.com/c/alexaentranced" {
				t.Fatalf("saved-context identity changed: %#v", got)
			}
			if environment != nil && !slices.Contains(diagnostics.IgnoredArguments, "environment") {
				t.Fatalf("environment omission was not diagnosed: %+v", diagnostics)
			}
		})
	}

	material := normalizeBrowserSessionArgs(map[string]any{
		"action": "open", "context_name": "Alexa Patreon",
		"environment": map[string]any{"timezone": "Europe/Paris"},
	})
	if got := material["environment"].(map[string]any)["timezone"]; got != "Europe/Paris" {
		t.Fatalf("materially different environment was discarded: %#v", material)
	}

	authorizedDefaults := normalizeBrowserSessionArgs(map[string]any{
		"action": "open", "context_name": "Alexa Patreon", "environment_override": true,
		"environment": map[string]any{"timezone": "UTC", "geolocation": map[string]any{"latitude": 0.0, "longitude": 0.0}},
	})
	if _, exists := authorizedDefaults["environment"]; !exists {
		t.Fatalf("explicitly authorized environment was discarded: %#v", authorizedDefaults)
	}
}

func TestBrowserSessionFiltersProductionStyleSynthesizedTemplate(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })
	fake := &fakeComp{display: backends.DisplaySize{Width: 1600, Height: 800}, url: "https://example.com/"}
	newBackend = func(_ backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action": "open", "auto_create_context": false, "backend": "local",
		"context_id": "", "context_name": "Saved Login", "persist": false,
		"presentation_mode": "fast", "provider_context_id": "", "proxy_country": "",
		"proxy_mode": "auto", "proxy_profile": "", "proxy_sticky": "rotating",
		"session_id": "", "tab_id": "", "timeout": float64(0),
		"url":      "https://example.com/account",
		"viewport": map[string]any{"width": float64(1600), "height": float64(800)},
		"environment": map[string]any{
			"device_scale_factor": float64(0.1),
			"geolocation":         map[string]any{"accuracy": float64(0), "latitude": float64(0), "longitude": float64(0), "permission": "prompt"},
			"languages":           []any{}, "locale": "", "max_touch_points": float64(1),
			"mobile": false, "timezone": "", "touch": false, "user_agent": "",
		},
	})
	if err != nil {
		t.Fatalf("synthesized session template should be safely filtered: %v", err)
	}
	result := out.(map[string]any)
	if applied, _ := result["environment_applied"].(bool); applied || !fake.openEnvironment.IsEmpty() {
		t.Fatalf("synthesized environment was applied: result=%#v backend=%+v", result, fake.openEnvironment)
	}
	ignored, ok := result["ignored_arguments"].([]string)
	if !ok {
		t.Fatalf("ignored arguments missing: %#v", result)
	}
	for _, want := range []string{"backend", "environment", "persist", "proxy_mode", "proxy_sticky", "viewport"} {
		if !slices.Contains(ignored, want) {
			t.Fatalf("ignored arguments %v missing %q", ignored, want)
		}
	}
	warnings, ok := result["argument_warnings"].([]string)
	if !ok || len(warnings) != 1 || !strings.Contains(warnings[0], "no browser emulation") {
		t.Fatalf("filter warning missing: %#v", result["argument_warnings"])
	}
	if result["persist"] != true {
		t.Fatalf("synthesized persist=false should fall back to the normal default: %#v", result["persist"])
	}
}

func TestBrowserSessionRejectsUnsupportedArgumentsAndUnsafeScale(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}

	if _, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "imaginary_mode": true}); err == nil || !strings.Contains(err.Error(), "unsupported_argument") || !strings.Contains(err.Error(), "imaginary_mode") {
		t.Fatalf("unknown argument should return a typed error: %v", err)
	}
	if _, err := app.toolBrowserSession(ctx, map[string]any{
		"action": "open", "environment_override": true, "environment": map[string]any{"device_scale_factor": float64(0.1)},
	}); err == nil || !strings.Contains(err.Error(), "between 0.5 and 4") {
		t.Fatalf("unsafe explicit device scale should be rejected: %v", err)
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
