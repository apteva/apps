package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type sessionArgumentDiagnostics struct {
	IgnoredArguments []string
	Warnings         []string
}

func (d *sessionArgumentDiagnostics) ignore(name string) {
	if name == "" {
		return
	}
	for _, existing := range d.IgnoredArguments {
		if existing == name {
			return
		}
	}
	d.IgnoredArguments = append(d.IgnoredArguments, name)
}

func (d *sessionArgumentDiagnostics) warn(message string) {
	for _, existing := range d.Warnings {
		if existing == message {
			return
		}
	}
	d.Warnings = append(d.Warnings, message)
}

func (d *sessionArgumentDiagnostics) finish() {
	sort.Strings(d.IgnoredArguments)
}

var browserSessionSupportedArguments = map[string]struct{}{
	"action": {}, "session_id": {}, "tab_id": {}, "backend": {}, "presentation_mode": {},
	"url": {}, "context_id": {}, "context_name": {}, "provider_context_id": {},
	"auto_create_context": {}, "persist": {}, "timeout": {}, "proxy_mode": {},
	"proxy_profile": {}, "proxy_country": {}, "proxy_sticky": {}, "environment": {},
	"environment_override": {}, "viewport": {},

	// Accepted compatibility aliases are intentionally not advertised in the
	// public schema, but remain supported for existing programmatic clients.
	"proxy": {}, "provider_context": {}, "user_agent": {}, "backend_session_id": {},
	"provider_session_id": {}, "region": {}, "backend_url": {}, "initial_url": {},
	"proxy_url": {}, "solve_captchas": {}, "use_proxy": {}, "block_ads": {},
	"solve_captcha": {}, "proxy_enabled": {}, "browser_project_id": {},
	"keep_alive": {}, "_reason": {}, "_project_id": {}, "project_id": {},
}

func validateBrowserSessionArguments(args map[string]any) error {
	var unsupported []string
	for key := range args {
		if _, ok := browserSessionSupportedArguments[key]; !ok {
			unsupported = append(unsupported, key)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	return fmt.Errorf("unsupported_argument: browser_session does not support %s; omit unknown fields and use the published tool schema", strings.Join(unsupported, ", "))
}

// normalizeBrowserSessionArgs removes optional values commonly synthesized by
// tool serializers even though the caller did not intend to configure them.
// It is deliberately limited to session arguments and preserves meaningful
// explicit overrides.
func normalizeBrowserSessionArgs(args map[string]any) map[string]any {
	out, _ := normalizeBrowserSessionArgsWithDiagnostics(args)
	return out
}

func normalizeBrowserSessionArgsWithDiagnostics(args map[string]any) (map[string]any, sessionArgumentDiagnostics) {
	out := make(map[string]any, len(args))
	for key, value := range args {
		if value != nil {
			out[key] = value
		}
	}
	var diagnostics sessionArgumentDiagnostics

	// Some models mechanically fill every optional schema property with an
	// empty, false, or boundary-looking value. This exact shape is not a real
	// device profile. Discard its default-looking overrides as one unit so a
	// schema minimum such as device_scale_factor=0.1 cannot distort the page.
	if looksLikeSynthesizedSessionTemplate(out) {
		for _, key := range []string{"environment", "backend", "presentation_mode", "proxy_mode", "proxy_sticky", "persist", "auto_create_context", "timeout", "viewport"} {
			if _, ok := out[key]; ok {
				delete(out, key)
				diagnostics.ignore(key)
			}
		}
		diagnostics.warn("Ignored mechanically populated browser-session defaults; no browser emulation was applied")
	}

	for _, key := range []string{
		"action", "session_id", "tab_id", "backend", "url",
		"context_id", "context_name", "provider_context_id", "provider_context",
		"presentation_mode", "proxy_mode", "proxy_profile", "proxy_country", "proxy_sticky",
		"region", "user_agent", "backend_url", "initial_url", "proxy_url",
		"backend_session_id", "provider_session_id",
	} {
		if value, ok := out[key].(string); ok {
			value = strings.TrimSpace(value)
			if value == "" {
				delete(out, key)
				diagnostics.ignore(key)
			} else {
				out[key] = value
			}
		}
	}
	if _, ok := out["_reason"]; ok {
		delete(out, "_reason")
		diagnostics.ignore("_reason")
	}
	if _, ok := out["keep_alive"]; ok {
		delete(out, "keep_alive")
		diagnostics.ignore("keep_alive")
	}
	if _, ok := out["timeout"]; ok && intArg(out, "timeout") <= 0 {
		delete(out, "timeout")
		diagnostics.ignore("timeout")
	}
	if value, ok := out["auto_create_context"].(bool); ok && !value {
		delete(out, "auto_create_context")
		diagnostics.ignore("auto_create_context")
	}
	if value, ok := out["presentation_mode"].(string); ok && value == "fast" {
		delete(out, "presentation_mode")
		diagnostics.ignore("presentation_mode")
	}

	if viewport, ok := out["viewport"].(map[string]any); ok {
		width, height := intArg(viewport, "width"), intArg(viewport, "height")
		if width <= 0 || height <= 0 || (width == 1600 && height == 800) {
			delete(out, "viewport")
			diagnostics.ignore("viewport")
		} else {
			out["viewport"] = map[string]any{"width": width, "height": height}
		}
	}

	if environment, ok := out["environment"].(map[string]any); ok {
		if normalized := normalizeSessionEnvironmentArgs(environment, &diagnostics); len(normalized) == 0 {
			delete(out, "environment")
			diagnostics.ignore("environment")
		} else {
			out["environment"] = normalized
		}
	}

	// proxy_mode is authoritative. Remove the deprecated boolean and selectors
	// that cannot apply to the selected mode instead of rejecting verbose calls.
	if mode := strings.ToLower(stringArg(out, "proxy_mode")); mode != "" {
		if _, ok := out["proxy"]; ok {
			delete(out, "proxy")
			diagnostics.ignore("proxy")
		}
		var irrelevant []string
		switch mode {
		case "auto", "direct":
			irrelevant = []string{"proxy_profile", "proxy_country", "proxy_sticky"}
		case "managed":
			irrelevant = []string{"proxy_profile", "proxy_sticky"}
		}
		for _, key := range irrelevant {
			if _, ok := out[key]; ok {
				delete(out, key)
				diagnostics.ignore(key)
			}
		}
	}

	filterBrowserSessionArgumentsForAction(out, &diagnostics)
	diagnostics.finish()
	return out, diagnostics
}

func normalizeSessionEnvironmentArgs(environment map[string]any, diagnostics *sessionArgumentDiagnostics) map[string]any {
	out := make(map[string]any, len(environment))
	for key, value := range environment {
		if value != nil {
			out[key] = value
		}
	}
	for _, key := range []string{"user_agent", "locale", "timezone"} {
		if value, ok := out[key].(string); ok {
			value = strings.TrimSpace(value)
			if value == "" {
				delete(out, key)
				diagnostics.ignore("environment." + key)
			} else {
				out[key] = value
			}
		}
	}
	if raw, ok := out["languages"]; ok {
		languages := stringSliceArg(map[string]any{"languages": raw}, "languages")
		if len(languages) == 0 {
			delete(out, "languages")
			diagnostics.ignore("environment.languages")
		} else {
			out["languages"] = languages
		}
	}
	if value, ok := numericArg(out["device_scale_factor"]); ok && value == 0 {
		delete(out, "device_scale_factor")
		diagnostics.ignore("environment.device_scale_factor")
	}
	for _, key := range []string{"mobile", "touch"} {
		if value, ok := out[key].(bool); ok && !value {
			delete(out, key)
			diagnostics.ignore("environment." + key)
		}
	}
	if touch, ok := out["touch"].(bool); !ok || !touch {
		if _, ok := out["max_touch_points"]; ok {
			delete(out, "max_touch_points")
			diagnostics.ignore("environment.max_touch_points")
		}
	}
	if geolocation, ok := out["geolocation"].(map[string]any); ok {
		geo := make(map[string]any, len(geolocation))
		for key, value := range geolocation {
			if value != nil {
				geo[key] = value
			}
		}
		if permission, ok := geo["permission"].(string); ok {
			permission = strings.TrimSpace(permission)
			if permission == "" {
				delete(geo, "permission")
				diagnostics.ignore("environment.geolocation.permission")
			} else {
				geo["permission"] = permission
			}
		}
		if len(geo) == 0 {
			delete(out, "geolocation")
			diagnostics.ignore("environment.geolocation")
		} else {
			out["geolocation"] = geo
		}
	}
	return out
}

func filterBrowserSessionArgumentsForAction(args map[string]any, diagnostics *sessionArgumentDiagnostics) {
	action := stringArg(args, "action")
	if action == "open" {
		for _, key := range []string{"session_id", "tab_id"} {
			if _, ok := args[key]; ok {
				delete(args, key)
				diagnostics.ignore(key)
			}
		}
		return
	}
	if action != "status" && action != "close" && action != "tabs" && action != "switch_tab" && action != "close_tab" && action != "list" {
		return
	}
	for _, key := range []string{
		"backend", "presentation_mode", "url", "context_id", "context_name", "provider_context_id", "provider_context",
		"auto_create_context", "persist", "timeout", "proxy", "proxy_mode", "proxy_profile", "proxy_country", "proxy_sticky",
		"environment", "environment_override", "viewport", "user_agent", "backend_session_id", "provider_session_id", "region", "backend_url", "initial_url", "proxy_url",
		"solve_captchas", "use_proxy", "block_ads", "solve_captcha", "proxy_enabled", "browser_project_id",
	} {
		if _, ok := args[key]; ok {
			delete(args, key)
			diagnostics.ignore(key)
		}
	}
	if action != "switch_tab" && action != "close_tab" {
		if _, ok := args["tab_id"]; ok {
			delete(args, "tab_id")
			diagnostics.ignore("tab_id")
		}
	}
}

func looksLikeSynthesizedSessionTemplate(args map[string]any) bool {
	environment, ok := args["environment"].(map[string]any)
	if !ok || !looksLikeSynthesizedEnvironmentTemplate(environment) {
		return false
	}
	signals := 0
	if sessionArgString(args["backend"]) == "local" {
		signals++
	}
	if sessionArgString(args["presentation_mode"]) == "fast" {
		signals++
	}
	if sessionArgString(args["proxy_mode"]) == "auto" {
		signals++
	}
	if sessionArgString(args["proxy_sticky"]) == "rotating" {
		signals++
	}
	if value, ok := args["persist"].(bool); ok && !value {
		signals++
	}
	if value, ok := args["auto_create_context"].(bool); ok && !value {
		signals++
	}
	if value, ok := numericArg(args["timeout"]); ok && value == 0 {
		signals++
	}
	if viewport, ok := args["viewport"].(map[string]any); ok && intArg(viewport, "width") == 1600 && intArg(viewport, "height") == 800 {
		signals++
	}
	return signals >= 4
}

func looksLikeSynthesizedEnvironmentTemplate(environment map[string]any) bool {
	if len(environment) < 7 {
		return false
	}
	dsf, dsfOK := numericArg(environment["device_scale_factor"])
	maxTouch, maxTouchOK := numericArg(environment["max_touch_points"])
	mobile, mobileOK := environment["mobile"].(bool)
	touch, touchOK := environment["touch"].(bool)
	return dsfOK && dsf == 0.1 && maxTouchOK && maxTouch == 1 &&
		mobileOK && !mobile && touchOK && !touch &&
		sessionArgString(environment["user_agent"]) == "" && sessionArgString(environment["locale"]) == "" &&
		sessionArgString(environment["timezone"]) == "" && emptySliceValue(environment["languages"])
}

func emptySliceValue(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	default:
		return false
	}
}

func sessionArgString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func numericArg(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
