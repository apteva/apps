package main

import (
	"errors"
	"regexp"
)

// Response storage policy for invocation records. The caller always gets
// the full response; this only decides what the invocation history keeps.
const (
	// storeResponsesAuto keeps unauthenticated responses and omits
	// authenticated ones. It is the default for every function.
	storeResponsesAuto       = "auto"
	storeResponsesNever      = "never"
	storeResponsesErrorsOnly = "errors_only"
	storeResponsesAlways     = "always"
)

func validateStoreResponses(value any) error {
	mode, _ := value.(string)
	switch mode {
	case storeResponsesAuto, storeResponsesNever, storeResponsesErrorsOnly, storeResponsesAlways:
		return nil
	default:
		return errors.New("store_responses must be auto, never, errors_only, or always")
	}
}

func storeResponsesSchema() map[string]any {
	return map[string]any{
		"type": "string", "enum": []string{storeResponsesAuto, storeResponsesNever, storeResponsesErrorsOnly, storeResponsesAlways},
		"description": "What invocation history keeps of each response body. auto (default): unauthenticated responses are kept, authenticated ones are not (status, size and timing only). never: no bodies. errors_only: bodies of failed calls only. always: every body. Bodies of authenticated calls and of errors_only records are kept only after anything that looks like a key or token is replaced with [redacted].",
	}
}

// responseStorage decides whether a finished invocation's response body is
// stored, and returns the body to store (possibly token-stripped) or the
// reason it was omitted.
func responseStorage(mode string, authenticated, ok bool, successDetailsDays int, body string) (stored string, omitted string) {
	if body == "" {
		return "", ""
	}
	if ok && successDetailsDays == 0 {
		return "", "success_details_disabled"
	}
	switch mode {
	case storeResponsesNever:
		return "", "policy_never"
	case storeResponsesErrorsOnly:
		if ok {
			return "", "errors_only"
		}
		return redactTokens(body), ""
	case storeResponsesAlways:
		if authenticated {
			return redactTokens(body), ""
		}
		return body, ""
	default: // auto
		if authenticated {
			return "", "authenticated"
		}
		return body, ""
	}
}

var (
	// "token": "...", api_key=..., Authorization: ... — the value after a
	// credential-looking name, in JSON, query-string or header form.
	credentialFieldRE = regexp.MustCompile(`(?i)("?(?:[a-z0-9_-]*(?:token|secret|password|passwd|api[_-]?key|apikey|private[_-]?key|access[_-]?key|client[_-]?secret|credential|authorization|cookie|session[_-]?id|signature))"?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|(?:(?:bearer|basic)\s+)?[^\s,&;{}\[\]"]+)`)
	// Bearer/Basic credentials in free text.
	authSchemeRE = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]{8,}`)
	// Well-known key and token shapes.
	tokenShapeRE = regexp.MustCompile(`\b(?:` +
		`sk-[A-Za-z0-9_-]{16,}|` + // OpenAI/Anthropic/Apteva style
		`(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{10,}|` + // Stripe
		`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|` + // GitHub
		`xox[abprs]-[A-Za-z0-9-]{10,}|` + // Slack
		`AKIA[0-9A-Z]{16}|` + // AWS access key id
		`AIza[0-9A-Za-z_-]{30,}|` + // Google API key
		`gwt_[A-Za-z0-9_-]{16,}|` + // Apteva gateway token
		`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}` + // JWT
		`)`)
)

// redactTokens replaces anything that looks like a key or token. It is a
// best-effort filter for stored history, not a guarantee: values with no
// recognizable name or shape are kept.
func redactTokens(s string) string {
	if s == "" {
		return s
	}
	s = credentialFieldRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := credentialFieldRE.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		if len(sub[2]) > 0 && sub[2][0] == '"' {
			return sub[1] + `"[redacted]"`
		}
		return sub[1] + "[redacted]"
	})
	s = authSchemeRE.ReplaceAllString(s, "$1 [redacted]")
	return tokenShapeRE.ReplaceAllString(s, "[redacted]")
}
