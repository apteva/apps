package main

import (
	"strings"
)

func sensitiveName(name string) bool {
	name = strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))
	for _, term := range []string{"authorization", "token", "password", "secret", "cookie", "apikey", "credential", "privatekey", "signature"} {
		if strings.Contains(name, term) {
			return true
		}
	}
	return false
}

func redactValue(v any, release *releaseSession) any {
	switch value := v.(type) {
	case string:
		if release != nil {
			for _, credential := range release.browserValues {
				if credential != "" && strings.Contains(value, credential) {
					return "[redacted]"
				}
			}
			for name, credential := range release.identities {
				token := strings.TrimPrefix(credential, release.config.Identities[name].Prefix)
				if token != "" && strings.Contains(value, token) {
					return "[redacted]"
				}
			}
		}
		if len(value) > 1024 {
			return "[value exceeds evidence limit]"
		}
		return value
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			if sensitiveName(k) {
				out[k] = "[redacted]"
			} else {
				out[k] = redactValue(item, release)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = redactValue(item, release)
		}
		return out
	default:
		return value
	}
}

func sanitizeStep(result *StepResult, output any, selected []string, release *releaseSession) {
	allowed := map[string]bool{"/status_code": true}
	if result.Kind == "browser" {
		// The protocol omits sensitive element values and redacts registered secrets.
		for _, path := range []string{"/count", "/visible", "/enabled", "/checked", "/target", "/path", "/text", "/value", "/action_dispatched"} {
			allowed[path] = true
		}
	}
	for _, path := range selected {
		if path == "/body" {
			continue
		}
		valid := true
		for _, segment := range strings.Split(path, "/") {
			if sensitiveName(segment) {
				valid = false
				break
			}
		}
		if valid {
			allowed[path] = true
		}
	}
	for i := range result.Assertions {
		assertion := &result.Assertions[i]
		if allowed[assertion.Path] {
			assertion.Value = redactValue(assertion.Value, release)
			assertion.Actual = redactValue(assertion.Actual, release)
			if len(encode(assertion.Actual)) > 2048 {
				assertion.Actual = "[value exceeds evidence limit]"
			}
			if len(encode(assertion.Value)) > 2048 {
				assertion.Value = "[value exceeds evidence limit]"
			}
		} else {
			assertion.Value, assertion.Actual = nil, nil
		}
	}
	result.Evidence = map[string]any{}
	for _, path := range selected {
		if value, exists := pointer(output, path); exists && allowed[path] {
			clean := redactValue(value, release)
			if len(encode(clean)) <= 2048 {
				result.Evidence[path] = clean
			}
		}
	}
	if release != nil {
		result.Correlation, _ = redactValue(result.Correlation, release).(string)
	}
}
