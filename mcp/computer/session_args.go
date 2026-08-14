package main

import (
	"strconv"
	"strings"
)

// normalizeBrowserSessionArgs removes optional values commonly synthesized by
// tool serializers even though the caller did not intend to configure them.
// It is deliberately limited to session-creation arguments: false remains
// meaningful for persist and for the legacy proxy field, while blank strings,
// zero timeouts, zero-sized viewports, and empty environment overrides are
// equivalent to omission.
func normalizeBrowserSessionArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		if value != nil {
			out[key] = value
		}
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
			} else {
				out[key] = value
			}
		}
	}
	if _, ok := out["timeout"]; ok && intArg(out, "timeout") <= 0 {
		delete(out, "timeout")
	}

	if viewport, ok := out["viewport"].(map[string]any); ok {
		width, height := intArg(viewport, "width"), intArg(viewport, "height")
		if width <= 0 || height <= 0 {
			delete(out, "viewport")
		} else {
			out["viewport"] = map[string]any{"width": width, "height": height}
		}
	}

	if environment, ok := out["environment"].(map[string]any); ok {
		if normalized := normalizeSessionEnvironmentArgs(environment); len(normalized) == 0 {
			delete(out, "environment")
		} else {
			out["environment"] = normalized
		}
	}
	return out
}

func normalizeSessionEnvironmentArgs(environment map[string]any) map[string]any {
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
			} else {
				out[key] = value
			}
		}
	}
	if raw, ok := out["languages"]; ok {
		languages := stringSliceArg(map[string]any{"languages": raw}, "languages")
		if len(languages) == 0 {
			delete(out, "languages")
		} else {
			out["languages"] = languages
		}
	}
	for _, key := range []string{"device_scale_factor", "max_touch_points"} {
		if value, ok := numericArg(out[key]); ok && value == 0 {
			delete(out, key)
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
			} else {
				geo["permission"] = permission
			}
		}
		if len(geo) == 0 {
			delete(out, "geolocation")
		} else {
			out["geolocation"] = geo
		}
	}

	// Optional booleans serialized as false do not constitute an environment
	// override on their own. This preserves the normal desktop/no-touch default
	// and prevents an otherwise-empty object from blocking service/attach flows.
	meaningful := false
	for key, value := range out {
		if key == "mobile" || key == "touch" {
			if enabled, ok := value.(bool); ok && !enabled {
				continue
			}
		}
		meaningful = true
		break
	}
	if !meaningful {
		return map[string]any{}
	}
	return out
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
