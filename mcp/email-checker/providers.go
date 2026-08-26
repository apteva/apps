package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const verificationProviderRole = "verification_providers"

type CheckOptions struct {
	SMTP         bool
	Timeout      time.Duration
	Provider     string
	ConnectionID int64
	IPAddress    string
}

type ProviderResult struct {
	Checked        bool     `json:"checked"`
	Provider       string   `json:"provider"`
	ConnectionID   int64    `json:"connection_id,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	Status         string   `json:"status,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Score          *float64 `json:"score,omitempty"`
	SuggestedEmail string   `json:"suggested_email,omitempty"`
	CatchAll       *bool    `json:"catch_all,omitempty"`
	Disposable     *bool    `json:"disposable,omitempty"`
	Role           *bool    `json:"role,omitempty"`
	Free           *bool    `json:"free,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type ProviderBinding struct {
	Provider     string `json:"provider"`
	ConnectionID int64  `json:"connection_id"`
	Default      bool   `json:"default"`
}

type ProviderList struct {
	Providers []ProviderBinding `json:"providers"`
}

type providerSpec struct {
	Tool  string
	Input func(email string, opts CheckOptions) map[string]any
}

var providerSpecs = map[string]providerSpec{
	"zerobounce": {
		Tool: "validate",
		Input: func(email string, opts CheckOptions) map[string]any {
			input := map[string]any{"email": email, "timeout": boundedSeconds(opts.Timeout, 3, 60)}
			if opts.IPAddress != "" {
				input["ip_address"] = opts.IPAddress
			}
			return input
		},
	},
	"bouncer": {
		Tool: "verify",
		Input: func(email string, opts CheckOptions) map[string]any {
			return map[string]any{"email": email, "timeout": boundedSeconds(opts.Timeout, 1, 30)}
		},
	},
	"neverbounce": {
		Tool: "single_check",
		Input: func(email string, opts CheckOptions) map[string]any {
			return map[string]any{"email": email, "timeout": boundedSeconds(opts.Timeout, 3, 60)}
		},
	},
	"kickbox": {
		Tool: "verify",
		Input: func(email string, opts CheckOptions) map[string]any {
			return map[string]any{"email": email, "timeout": boundedSeconds(opts.Timeout, 1, 30) * 1000}
		},
	},
	"millionverifier": {
		Tool: "verify",
		Input: func(email string, opts CheckOptions) map[string]any {
			return map[string]any{"email": email, "timeout": boundedSeconds(opts.Timeout, 2, 60)}
		},
	},
	"hunter": {
		Tool: "email_verifier",
		Input: func(email string, _ CheckOptions) map[string]any {
			return map[string]any{"email": email}
		},
	},
}

func runCheck(ctx *sdk.AppCtx, input string, opts CheckOptions) (CheckResult, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	result := check(input, opts.SMTP, opts.Timeout)
	requested := strings.ToLower(strings.TrimSpace(opts.Provider))
	if requested == "" || requested == "local" {
		return result, nil
	}
	if _, known := providerSpecs[requested]; !known && requested != "auto" {
		return result, fmt.Errorf("unsupported verification provider %q", requested)
	}

	// Do not spend provider credits on addresses that already fail the cheap,
	// deterministic gate. A caller still gets a complete local result and an
	// explicit explanation of why no provider request was made.
	if localFailureIsDefinitive(result) {
		result.Provider = &ProviderResult{
			Checked:        false,
			Provider:       requested,
			Verdict:        "undeliverable",
			Recommendation: "do_not_send",
			Reason:         "local_checks_failed",
		}
		return result, nil
	}

	bound, err := selectVerificationProvider(ctx, requested, opts.ConnectionID)
	if err != nil {
		return result, err
	}
	providerResult := executeVerificationProvider(ctx, bound, result.Email, opts)
	result.Provider = &providerResult
	if providerResult.Verdict != "" {
		result.Verdict = providerResult.Verdict
		result.Recommendation = providerResult.Recommendation
		switch providerResult.Verdict {
		case "deliverable", "undeliverable":
			result.Confidence = "high"
		case "risky":
			result.Confidence = "medium"
		default:
			result.Confidence = "low"
		}
		// Preserve the legacy boolean for callers that have not adopted the
		// richer verdict yet. Once a provider was requested, only its positive
		// deliverability verdict should leave valid=true.
		result.Valid = providerResult.Verdict == "deliverable"
	}
	return result, nil
}

func applyLocalDecision(result *CheckResult) {
	switch {
	case localFailureIsDefinitive(*result):
		result.Valid = false
		result.Verdict = "undeliverable"
		result.Confidence = "high"
		result.Recommendation = "do_not_send"
	case result.DomainStatus == "dns_error":
		result.Valid = false
		result.Verdict = "unknown"
		result.Confidence = "low"
		result.Recommendation = "retry"
	case result.SMTP.Checked && result.SMTP.Informative != nil && *result.SMTP.Informative && result.SMTP.RcptStatus == "reject":
		result.Valid = false
		result.Verdict = "undeliverable"
		result.Confidence = "high"
		result.Recommendation = "do_not_send"
	case result.SuggestedEmail != "":
		result.Verdict = "risky"
		result.Confidence = "medium"
		result.Recommendation = "review"
	case result.SMTP.Checked && result.SMTP.Informative != nil && *result.SMTP.Informative && result.SMTP.RcptStatus == "ok":
		result.Verdict = "deliverable"
		result.Confidence = "high"
		result.Recommendation = "send"
	case result.SMTP.Checked && result.SMTP.RcptStatus == "catch_all":
		result.Verdict = "risky"
		result.Confidence = "low"
		result.Recommendation = "review"
	case result.SMTP.Checked && result.SMTP.Retryable:
		result.Verdict = "unknown"
		result.Confidence = "low"
		result.Recommendation = "retry"
	default:
		result.Verdict = "unknown"
		result.Confidence = "low"
		result.Recommendation = "run_smtp_probe"
	}
}

func localFailureIsDefinitive(result CheckResult) bool {
	return !result.SyntaxOK || result.Disposable || result.DomainStatus == "null_mx" || result.DomainStatus == "no_mail_server"
}

func listVerificationProviders(ctx *sdk.AppCtx) ProviderList {
	list := ProviderList{Providers: []ProviderBinding{}}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return list
	}
	for _, bound := range ctx.IntegrationsFor(verificationProviderRole) {
		if bound == nil || bound.ConnectionID <= 0 {
			continue
		}
		slug := strings.ToLower(strings.TrimSpace(bound.AppSlug))
		if _, supported := providerSpecs[slug]; !supported {
			continue
		}
		list.Providers = append(list.Providers, ProviderBinding{
			Provider:     slug,
			ConnectionID: bound.ConnectionID,
			Default:      bound.IsDefault,
		})
	}
	return list
}

func selectVerificationProvider(ctx *sdk.AppCtx, requested string, connectionID int64) (*sdk.BoundIntegration, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, fmt.Errorf("verification provider %q requested but app context is unavailable", requested)
	}
	bindings := ctx.IntegrationsFor(verificationProviderRole)
	if len(bindings) == 0 {
		return nil, fmt.Errorf("verification provider %q requested but no compatible provider is bound", requested)
	}
	for _, bound := range bindings {
		if bound == nil {
			continue
		}
		slug := strings.ToLower(strings.TrimSpace(bound.AppSlug))
		if connectionID > 0 && bound.ConnectionID != connectionID {
			continue
		}
		if requested != "auto" && slug != requested {
			continue
		}
		if requested == "auto" && connectionID == 0 && !bound.IsDefault {
			continue
		}
		if _, supported := providerSpecs[slug]; supported {
			return bound, nil
		}
	}
	if connectionID > 0 {
		return nil, fmt.Errorf("connection_id %d is not a bound %s verification provider", connectionID, requested)
	}
	return nil, fmt.Errorf("verification provider %q is not bound", requested)
}

func executeVerificationProvider(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, email string, opts CheckOptions) ProviderResult {
	slug := strings.ToLower(strings.TrimSpace(bound.AppSlug))
	result := ProviderResult{Checked: true, Provider: slug, ConnectionID: bound.ConnectionID}
	spec, ok := providerSpecs[slug]
	if !ok {
		result.Error = "unsupported provider binding"
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	response, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, spec.Tool, spec.Input(email, opts))
	if err != nil {
		result.Error = err.Error()
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	if response == nil {
		result.Error = "provider returned no response"
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	if !response.Success {
		result.Error = providerError(response.Status, response.Data)
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Data, &payload); err != nil {
		result.Error = "provider returned invalid JSON"
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	if message := providerPayloadError(payload); message != "" {
		result.Error = message
		result.Verdict = "unknown"
		result.Recommendation = "review"
		return result
	}
	return normalizeProviderResponse(slug, payload, bound.ConnectionID)
}

func normalizeProviderResponse(slug string, payload map[string]any, connectionID int64) ProviderResult {
	result := ProviderResult{Checked: true, Provider: slug, ConnectionID: connectionID}
	data := payload
	if nested, ok := payload["data"].(map[string]any); ok {
		data = nested
	}

	switch slug {
	case "zerobounce":
		result.Status = normalizedString(data["status"])
		result.Reason = normalizedString(data["sub_status"])
		result.SuggestedEmail = stringValue(data["did_you_mean"])
		result.CatchAll = boolValueAny(data["catchall_domain"])
		result.Disposable = boolPtr(strings.Contains(result.Reason, "disposable"))
		result.Role = boolPtr(strings.HasPrefix(result.Reason, "role_based"))
		result.Free = boolValueAny(data["free_email"])
		switch result.Status {
		case "valid":
			setProviderDecision(&result, "deliverable", "send")
		case "invalid":
			setProviderDecision(&result, "undeliverable", "do_not_send")
		case "spamtrap", "abuse", "do_not_mail":
			setProviderDecision(&result, "risky", "do_not_send")
		case "catch_all", "catch-all":
			setProviderDecision(&result, "risky", "review")
		default:
			setProviderDecision(&result, "unknown", "review")
		}

	case "bouncer":
		result.Status = normalizedString(data["status"])
		result.Reason = normalizedString(data["reason"])
		result.Score = floatValue(data["score"])
		result.CatchAll = yesNoValue(nestedValue(data, "domain", "acceptAll"))
		result.Disposable = yesNoValue(nestedValue(data, "domain", "disposable"))
		result.Free = yesNoValue(nestedValue(data, "domain", "free"))
		result.Role = yesNoValue(nestedValue(data, "account", "role"))
		setProviderDecision(&result, mapDirectVerdict(result.Status), recommendationFor(result.Status, result.Disposable))

	case "neverbounce":
		result.Status = normalizedString(data["result"])
		result.Reason = stringSliceValue(data["flags"])
		result.CatchAll = boolPtr(result.Status == "catchall" || strings.Contains(result.Reason, "accepts_all"))
		result.Disposable = boolPtr(result.Status == "disposable" || strings.Contains(result.Reason, "disposable_email"))
		result.Role = boolPtr(strings.Contains(result.Reason, "role_account"))
		result.Free = boolPtr(strings.Contains(result.Reason, "free_email_host"))
		switch result.Status {
		case "valid":
			setProviderDecision(&result, "deliverable", "send")
		case "invalid":
			setProviderDecision(&result, "undeliverable", "do_not_send")
		case "disposable":
			setProviderDecision(&result, "risky", "do_not_send")
		case "catchall":
			setProviderDecision(&result, "risky", "review")
		default:
			setProviderDecision(&result, "unknown", "review")
		}

	case "kickbox":
		result.Status = normalizedString(data["result"])
		result.Reason = normalizedString(data["reason"])
		result.Score = floatValue(data["sendex"])
		result.SuggestedEmail = stringValue(data["did_you_mean"])
		result.CatchAll = boolValueAny(firstValue(data, "accept_all", "acceptAll"))
		result.Disposable = boolValueAny(data["disposable"])
		result.Role = boolValueAny(data["role"])
		result.Free = boolValueAny(firstValue(data, "free", "free_email"))
		setProviderDecision(&result, mapDirectVerdict(result.Status), recommendationFor(result.Status, result.Disposable))

	case "millionverifier":
		result.Status = normalizedString(data["result"])
		result.Reason = normalizedString(data["subresult"])
		result.SuggestedEmail = stringValue(data["didyoumean"])
		result.Disposable = boolPtr(result.Status == "disposable")
		result.CatchAll = boolPtr(result.Status == "catch_all" || result.Status == "catchall")
		result.Role = boolValueAny(data["role"])
		result.Free = boolValueAny(data["free"])
		switch result.Status {
		case "ok", "valid":
			setProviderDecision(&result, "deliverable", "send")
		case "invalid", "error":
			setProviderDecision(&result, "undeliverable", "do_not_send")
		case "disposable":
			setProviderDecision(&result, "risky", "do_not_send")
		case "catch_all", "catchall":
			setProviderDecision(&result, "risky", "review")
		default:
			setProviderDecision(&result, "unknown", "review")
		}

	case "hunter":
		result.Status = normalizedString(data["status"])
		result.Score = floatValue(data["score"])
		result.CatchAll = boolValueAny(data["accept_all"])
		result.Disposable = boolValueAny(data["disposable"])
		result.Free = boolValueAny(data["webmail"])
		switch result.Status {
		case "valid", "webmail":
			setProviderDecision(&result, "deliverable", "send")
		case "invalid":
			setProviderDecision(&result, "undeliverable", "do_not_send")
		case "disposable":
			setProviderDecision(&result, "risky", "do_not_send")
		case "accept_all", "accept-all":
			setProviderDecision(&result, "risky", "review")
		default:
			setProviderDecision(&result, "unknown", "review")
		}

	default:
		result.Error = "unsupported provider response"
		setProviderDecision(&result, "unknown", "review")
	}
	return result
}

func setProviderDecision(result *ProviderResult, verdict, recommendation string) {
	result.Verdict = verdict
	result.Recommendation = recommendation
}

func mapDirectVerdict(status string) string {
	switch status {
	case "deliverable":
		return "deliverable"
	case "undeliverable":
		return "undeliverable"
	case "risky":
		return "risky"
	default:
		return "unknown"
	}
}

func recommendationFor(status string, disposable *bool) string {
	if disposable != nil && *disposable {
		return "do_not_send"
	}
	switch status {
	case "deliverable":
		return "send"
	case "undeliverable":
		return "do_not_send"
	default:
		return "review"
	}
}

func boundedSeconds(timeout time.Duration, min, max int) int {
	seconds := int(timeout / time.Second)
	if seconds < min {
		return min
	}
	if seconds > max {
		return max
	}
	return seconds
}

func providerError(status int, data json.RawMessage) string {
	message := ""
	var payload map[string]any
	if json.Unmarshal(data, &payload) == nil {
		for _, key := range []string{"error", "message", "error_message"} {
			if value := strings.TrimSpace(stringValue(payload[key])); value != "" {
				message = value
				break
			}
		}
	}
	if message == "" {
		message = "provider request failed"
	}
	if status > 0 {
		return fmt.Sprintf("%s (HTTP %d)", message, status)
	}
	return message
}

func providerPayloadError(payload map[string]any) string {
	for _, key := range []string{"error", "error_message"} {
		if value, exists := payload[key]; exists {
			if message := strings.TrimSpace(stringValue(value)); message != "" && message != "<nil>" {
				return message
			}
		}
	}
	if success, exists := payload["success"].(bool); exists && !success {
		if message := strings.TrimSpace(stringValue(payload["message"])); message != "" && message != "<nil>" {
			return message
		}
		return "provider reported an unsuccessful verification"
	}
	if _, hasStatus := payload["status"]; !hasStatus {
		if _, hasResult := payload["result"]; !hasResult {
			if _, hasData := payload["data"]; !hasData {
				if message := strings.TrimSpace(stringValue(payload["message"])); message != "" && message != "<nil>" {
					return message
				}
			}
		}
	}
	return ""
}

func normalizedString(value any) string {
	return strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(stringValue(value))))
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSliceValue(value any) string {
	items, ok := value.([]any)
	if !ok {
		return normalizedString(value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := normalizedString(item); text != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, ",")
}

func floatValue(value any) *float64 {
	switch number := value.(type) {
	case float64:
		return &number
	case float32:
		converted := float64(number)
		return &converted
	case int:
		converted := float64(number)
		return &converted
	case int64:
		converted := float64(number)
		return &converted
	default:
		return nil
	}
}

func boolValueAny(value any) *bool {
	switch flag := value.(type) {
	case bool:
		return boolPtr(flag)
	case string:
		return yesNoValue(flag)
	default:
		return nil
	}
}

func yesNoValue(value any) *bool {
	switch normalizedString(value) {
	case "yes", "true", "1":
		return boolPtr(true)
	case "no", "false", "0":
		return boolPtr(false)
	default:
		return nil
	}
}

func nestedValue(data map[string]any, objectKey, valueKey string) any {
	object, _ := data[objectKey].(map[string]any)
	return object[valueKey]
}

func firstValue(data map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}
