package main

import (
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const idempotentUpdateAttempts = 3

type integrationExecPolicy struct {
	maxAttempts int
}

type metaErrorEnvelope struct {
	Error struct {
		Code        int    `json:"code"`
		Subcode     int    `json:"error_subcode"`
		Type        string `json:"type"`
		Message     string `json:"message"`
		IsTransient bool   `json:"is_transient"`
		TraceID     string `json:"fbtrace_id"`
	} `json:"error"`
}

func (a *App) execIntegrationToolWithPolicy(
	ctx *sdk.AppCtx,
	acct *adAccount,
	tool string,
	input map[string]any,
	policy integrationExecPolicy,
) (any, map[string]any) {
	attempts := policy.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		parsed, errOut := a.execIntegrationToolOnce(ctx, acct, tool, input)
		if errOut == nil {
			return parsed, nil
		}
		errOut["attempts"] = attempt
		if attempt == attempts || errOut["retryable"] != true {
			return nil, errOut
		}
		if !a.sleepBeforeRetry(ctx, a.delayBeforeRetry(attempt)) {
			errOut["code"] = "operation_cancelled"
			errOut["retryable"] = false
			return nil, errOut
		}
	}
	return nil, mcpError(tool + ": provider request failed")
}

func (a *App) execIntegrationToolOnce(
	ctx *sdk.AppCtx,
	acct *adAccount,
	tool string,
	input map[string]any,
) (any, map[string]any) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnectionID, tool, input)
	if err != nil {
		return nil, mcpError(tool + ": " + err.Error())
	}
	if res == nil || !res.Success {
		return nil, classifyProviderFailure(acct, tool, res)
	}
	var parsed any
	if len(res.Data) > 0 {
		if err := json.Unmarshal(res.Data, &parsed); err != nil {
			return nil, mcpError(tool + ": parse: " + err.Error())
		}
	}
	return parsed, nil
}

func classifyProviderFailure(acct *adAccount, tool string, res *sdk.ExecuteResult) map[string]any {
	body := ""
	if res != nil {
		body = string(res.Data)
	}
	out := mcpError(tool + ": upstream non-2xx: " + body)
	out["retryable"] = false
	if res != nil {
		out["provider_status"] = res.Status
	}

	if acct != nil && acct.Platform == "meta" {
		var envelope metaErrorEnvelope
		if json.Unmarshal([]byte(body), &envelope) == nil && envelope.Error.Message != "" {
			providerErr := envelope.Error
			out = mcpError(tool + ": " + providerErr.Message)
			out["provider_status"] = 0
			if res != nil {
				out["provider_status"] = res.Status
			}
			out["provider_code"] = providerErr.Code
			out["provider_type"] = providerErr.Type
			out["provider_message"] = providerErr.Message
			out["retryable"] = false
			if providerErr.Subcode != 0 {
				out["provider_subcode"] = providerErr.Subcode
			}
			if providerErr.TraceID != "" {
				out["fbtrace_id"] = providerErr.TraceID
			}
			switch {
			case providerErr.Code == 190:
				out["code"] = "provider_auth_error"
			case providerErr.IsTransient:
				out["code"] = "provider_transient"
				out["retryable"] = true
			}
		}
	}

	if out["code"] != "provider_auth_error" && providerRateLimited(res, body) {
		out["code"] = "provider_rate_limited"
		out["retryable"] = true
	}
	return out
}

func (a *App) execIdempotentUpdate(
	ctx *sdk.AppCtx,
	acct *adAccount,
	tool string,
	input map[string]any,
) (any, map[string]any) {
	return a.execIntegrationToolWithPolicy(ctx, acct, tool, input, integrationExecPolicy{
		maxAttempts: idempotentUpdateAttempts,
	})
}

func (a *App) execUpdateOrErr(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (any, error) {
	parsed, errOut := a.execIdempotentUpdate(ctx, acct, tool, input)
	if errOut != nil {
		return errOut, nil
	}
	return parsed, nil
}

func (a *App) delayBeforeRetry(retry int) time.Duration {
	if a.retryDelay != nil {
		return a.retryDelay(retry)
	}
	base := 100 * time.Millisecond
	if retry > 1 {
		base *= time.Duration(1 << min(retry-1, 4))
	}
	// Provider retries should not synchronize across app instances.
	jitter := time.Duration(time.Now().UnixNano()%50) * time.Millisecond
	return base + jitter
}

func (a *App) sleepBeforeRetry(ctx *sdk.AppCtx, delay time.Duration) bool {
	if a.sleep != nil {
		return a.sleep(ctx, delay)
	}
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func providerFailureText(errOut map[string]any) string {
	if message := stringArgAny(errOut, "provider_message"); message != "" {
		return message
	}
	content, _ := errOut["content"].([]map[string]any)
	if len(content) > 0 {
		return fmt.Sprint(content[0]["text"])
	}
	return "provider request failed"
}
