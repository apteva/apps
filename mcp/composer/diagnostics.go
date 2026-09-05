package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	bearerSecretPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s"']+`)
	jsonSecretPattern   = regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|signature|x-amz-signature)"\s*:\s*")[^"]*(")`)
	querySecretPattern  = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|signature|x-amz-signature|x-goog-signature)=)[^&\s"']+`)
	envSecretPattern    = regexp.MustCompile(`(?i)((?:APTEVA_(?:APP|OUTBOUND)_TOKEN|API_KEY|ACCESS_TOKEN|AUTH_TOKEN|SECRET|PASSWORD)\s*=\s*)[^\s"']+`)
)

const redactedValue = "[REDACTED]"

// redactSecrets removes common credentials from renderer commands, provider
// responses, and stderr before they are persisted, emitted, or shown in the UI.
func redactSecrets(s string) string {
	if s == "" {
		return ""
	}
	s = bearerSecretPattern.ReplaceAllString(s, `${1}`+redactedValue)
	s = jsonSecretPattern.ReplaceAllString(s, `${1}`+redactedValue+`${2}`)
	s = querySecretPattern.ReplaceAllString(s, `${1}`+redactedValue)
	s = envSecretPattern.ReplaceAllString(s, `${1}`+redactedValue)
	return s
}

func renderFailureDetail(err error, executor, phase string) (string, string) {
	message := "Render failed."
	detail := ""
	if err != nil {
		detail = strings.TrimSpace(redactSecrets(err.Error()))
		if first, _, ok := strings.Cut(detail, "\n"); ok {
			message = strings.TrimSpace(first)
		} else if detail != "" {
			message = detail
		}
	}
	if len(message) > 300 {
		message = message[:300] + "…"
	}
	if len(detail) > 8192 {
		detail = truncTail(detail, 8192)
	}
	diagnostic := map[string]any{
		"message":  message,
		"stage":    firstNonEmpty(strings.TrimSpace(phase), "rendering"),
		"executor": firstNonEmpty(strings.TrimSpace(executor), "unknown"),
		"detail":   detail,
	}
	raw, _ := json.Marshal(diagnostic)
	return message, string(raw)
}
