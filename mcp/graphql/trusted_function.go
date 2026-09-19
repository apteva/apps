package main

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type trustedFunctionConfig struct {
	Authenticated bool    `json:"authenticated"`
	FunctionID    int64   `json:"function_id"`
	FunctionIDs   []int64 `json:"function_ids"`
	Contract      string  `json:"contract"`
}

func functionSecurity(config map[string]any) (trustedFunctionConfig, error) {
	var c trustedFunctionConfig
	// Decode only these reserved configuration fields. Arguments never enter it.
	raw := map[string]any{}
	for _, k := range []string{"authenticated", "function_id", "function_ids", "contract"} {
		if v, ok := config[k]; ok {
			raw[k] = v
		}
	}
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, invalid("invalid Function security configuration")
	}
	if c.Contract == "" {
		c.Contract = "graphql"
	}
	if c.Contract != "graphql" && c.Contract != "http" {
		return c, invalid("Function contract must be graphql or http")
	}
	if !c.Authenticated {
		if c.Contract != "graphql" {
			return c, invalid("HTTP-compatible contract requires authenticated invocation")
		}
		return c, nil
	}
	if c.FunctionID <= 0 || len(c.FunctionIDs) == 0 || len(c.FunctionIDs) > 100 || !slices.Contains(c.FunctionIDs, c.FunctionID) {
		return c, invalid("authenticated Function requires function_id and an explicit function_ids allowlist containing it")
	}
	seen := map[int64]bool{}
	for _, id := range c.FunctionIDs {
		if id <= 0 || seen[id] {
			return c, invalid("Function IDs must be unique positive integers")
		}
		seen[id] = true
	}
	return c, nil
}

func (a *App) callTrustedFunction(ctx context.Context, config map[string]any, args map[string]any, c trustedFunctionConfig) (any, error) {
	identity := securityIdentity(ctx)
	project, _ := config["_project_id"].(string)
	if identity == nil || identity.Project != project || !time.Now().Before(identity.Expires) {
		return nil, unauthenticated()
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		return nil, unauthenticated()
	}
	if identity.Expires.Before(deadline) {
		deadline = identity.Expires
	}
	claims := map[string]any{}
	for k, v := range identity.Claims {
		claims[k] = v
	}
	claims["tenant_id"] = identity.Tenant
	event := map[string]any{"arguments": args, "parent": config["parent"], "project_id": project}
	if c.Contract == "http" {
		event = map[string]any{"body": args}
	}
	// Functions injects the authoritative authorizer; no tokens, browser headers,
	// requestContext, or caller-provided invocation metadata are forwarded.
	input := map[string]any{
		"id": c.FunctionID, "_project_id": project,
		"principal": map[string]any{"subject": identity.Subject, "issuer": identity.Issuer, "project_id": project, "function_ids": append([]int64(nil), c.FunctionIDs...), "claims": claims},
		"event":     event, "deadline": deadline.UTC().Format(time.RFC3339Nano), "request_id": identity.RequestID,
	}
	var out struct {
		Status    string  `json:"status"`
		Response  *string `json:"response"`
		ErrorCode string  `json:"error_code"`
	}
	start := time.Now()
	err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(project).PlatformAPI(), "functions", "functions_invoke_authenticated", input, &out)
	if err != nil {
		a.ctx.Logger().Info("trusted Function rejected", "request_id", identity.RequestID, "function_id", c.FunctionID, "duration_ms", time.Since(start).Milliseconds())
		return nil, forbidden("trusted Function invocation failed or was denied")
	}
	if out.Status != "ok" || out.Response == nil || len(*out.Response) > 8<<20 {
		return nil, internal("trusted Function execution failed")
	}
	var value any
	if json.Unmarshal([]byte(*out.Response), &value) != nil {
		return nil, internal("Function returned invalid JSON")
	}
	if c.Contract == "http" {
		envelope, ok := value.(map[string]any)
		if !ok {
			return nil, internal("invalid HTTP-compatible Function response")
		}
		status, ok := envelope["statusCode"].(float64)
		if !ok || math.Trunc(status) != status {
			return nil, internal("invalid Function status code")
		}
		if status == 401 {
			return nil, unauthenticated()
		}
		if status == 403 {
			return nil, forbidden("Function access denied")
		}
		if status < 200 || status >= 300 {
			return nil, internal("Function returned an unsuccessful status")
		}
		value = envelope["body"]
		if body, ok := value.(string); ok {
			if json.Unmarshal([]byte(body), &value) != nil {
				return nil, internal("Function body must be JSON")
			}
		}
	}
	a.ctx.Logger().Info("trusted Function completed", "request_id", identity.RequestID, "function_id", c.FunctionID, "duration_ms", time.Since(start).Milliseconds())
	return value, nil
}
