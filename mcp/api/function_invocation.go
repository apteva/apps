package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// Functions owns this admission schema. Scope comes exclusively from API
// configuration, never from browser fields or an authorizer's response.
type functionPrincipal struct {
	Issuer      string         `json:"issuer"`
	Subject     string         `json:"subject"`
	ProjectID   string         `json:"project_id"`
	FunctionIDs []int64        `json:"function_ids"`
	Claims      map[string]any `json:"claims,omitempty"`
}

type authenticatedFunctionResult struct {
	Status    string  `json:"status"`
	Response  *string `json:"response"`
	ErrorCode string  `json:"error_code"`
}

func (a *App) dispatchAuthenticatedFunction(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute, event map[string]any, auth authContext) (int, error) {
	policy, err := effectiveAuthPolicy(api.AuthJSON, route.AuthJSON)
	if err != nil || policy.Kind == "public" || len(policy.FunctionIDs) == 0 || auth.Principal == nil || auth.Principal.ProjectID != api.ProjectID {
		httpErr(w, http.StatusServiceUnavailable, "authenticated Function route requires a verified principal and configured auth.function_ids")
		return http.StatusServiceUnavailable, errors.New("invalid authenticated Function route configuration")
	}
	deadline, ok := r.Context().Deadline()
	if !ok {
		httpErr(w, http.StatusServiceUnavailable, "authenticated Function route requires a request deadline")
		return http.StatusServiceUnavailable, errors.New("missing authenticated Function request deadline")
	}
	if !deadline.After(time.Now()) {
		return writeGatewayFailure(w, r.Context(), errGatewayDeadline)
	}
	principal := functionPrincipal{Issuer: auth.Principal.Issuer, Subject: auth.Principal.Subject, ProjectID: api.ProjectID, FunctionIDs: append([]int64(nil), policy.FunctionIDs...), Claims: map[string]any{}}
	for key, value := range auth.Principal.Claims {
		principal.Claims[key] = value
	}
	// Functions accepts tenant information as an opaque claim. This reserved
	// claim is set from verified identity, not from arbitrary provider claims.
	if auth.Principal.TenantID != "" {
		principal.Claims["tenant_id"] = auth.Principal.TenantID
	}
	// Functions constructs requestContext.authorizer after admission. The
	// event contains only the request envelope and legacy non-authoritative auth.
	delete(event, "principal")
	delete(event, "requestContext")
	payload := map[string]any{
		"tool": "functions_invoke_authenticated",
		"input": map[string]any{
			"name": route.TargetRef, "_project_id": api.ProjectID,
			"principal": principal, "event": event,
			"request_id": gatewayRequestID(r.Context()), "deadline": deadline.UTC().Format(time.RFC3339Nano),
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return writeGatewayFailure(w, r.Context(), err)
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := outboundAppToken()
	if base == "" || token == "" {
		return writeGatewayFailure(w, r.Context(), errors.New("platform app credentials unavailable"))
	}
	// CallAppResult has no request-context variant in SDK v0.79.0. Use the same
	// MCP callback explicitly so browser cancellation and the original deadline
	// cover transport and response reading, with bounded envelope decoding below.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/api/apps/callback/apps/functions/call", bytes.NewReader(raw))
	if err != nil {
		return writeGatewayFailure(w, r.Context(), err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", gatewayRequestID(r.Context()))
	// Caller-installation headers are minted by the platform. API must never
	// claim an installation identity in the request body or forwarding headers.
	resp, err := a.performRequest(req)
	if err != nil {
		return writeGatewayFailure(w, r.Context(), err)
	}
	defer resp.Body.Close()
	// The MCP content text is itself JSON-encoded, so permit wire escaping
	// overhead but separately bound the decoded Function response below.
	wire, err := readBounded(resp.Body, 8*maxFunctionResponseBytes)
	if err != nil {
		return writeGatewayFailure(w, r.Context(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := http.StatusBadGateway
		switch resp.StatusCode {
		case 401, 403:
			status = 403
		case 408, 504:
			return writeGatewayFailure(w, r.Context(), &gatewayFailure{"upstream_timeout", "Authenticated Function invocation timed out", 504, nil})
		case 429, 503:
			status = 503
		}
		httpErr(w, status, "authenticated Function invocation unavailable or denied")
		return status, errors.New("authenticated Function callback failed")
	}
	result, err := decodeAuthenticatedFunctionResult(wire)
	if err != nil {
		var failure *gatewayFailure
		if errors.As(err, &failure) {
			if failure.status == 504 {
				return writeGatewayFailure(w, r.Context(), err)
			}
			httpErr(w, failure.status, failure.message)
			return failure.status, err
		}
		return writeGatewayFailure(w, r.Context(), err)
	}
	if result.Status != "ok" {
		rawResult, _ := json.Marshal(result)
		if failure := functionDeadlineFailure(500, rawResult); failure != nil {
			return writeGatewayFailure(w, r.Context(), failure)
		}
		httpErr(w, 502, "authenticated Function execution failed")
		return 502, errors.New("authenticated Function execution failed")
	}
	if result.Response == nil || len(*result.Response) > int(maxFunctionResponseBytes) {
		return writeGatewayFailure(w, r.Context(), errors.New("invalid or oversized authenticated Function response"))
	}
	body := []byte(*result.Response)
	capture := &statusCapture{ResponseWriter: w}
	if json.Valid(body) && writeStructuredResponse(capture, body) {
		return capture.status, nil
	}
	if json.Valid(body) {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(200)
	_, err = w.Write(body)
	return 200, err
}

// Accept the platform's JSON-RPC envelope, bare MCP result, or an already
// unwrapped result. Tool/RPC errors can never become successful HTTP bodies.
func decodeAuthenticatedFunctionResult(raw []byte) (authenticatedFunctionResult, error) {
	var zero authenticatedFunctionResult
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return zero, errors.New("invalid authenticated Function result")
	}
	if rpcError, ok := object["error"]; ok && len(object["jsonrpc"]) > 0 && string(rpcError) != "null" {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rpcError, &failure)
		return zero, authenticatedFunctionToolError(failure.Message)
	}
	if result, ok := object["result"]; ok {
		return decodeAuthenticatedFunctionContent(result)
	}
	return decodeAuthenticatedFunctionContent(raw)
}
func decodeAuthenticatedFunctionContent(raw []byte) (authenticatedFunctionResult, error) {
	var zero authenticatedFunctionResult
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return zero, errors.New("invalid authenticated Function result")
	}
	if content, ok := object["content"]; ok {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
			return zero, errors.New("invalid authenticated Function MCP content")
		}
		var isError bool
		if value, ok := object["isError"]; ok && json.Unmarshal(value, &isError) != nil {
			return zero, errors.New("invalid authenticated Function MCP status")
		}
		if isError {
			return zero, authenticatedFunctionToolError(blocks[0].Text)
		}
		raw = []byte(blocks[0].Text)
	} else if _, ok := object["isError"]; ok {
		return zero, errors.New("authenticated Function MCP result has no content")
	}
	var result authenticatedFunctionResult
	if json.Unmarshal(raw, &result) != nil || result.Status == "" {
		return zero, errors.New("invalid authenticated Function invocation result")
	}
	return result, nil
}
func authenticatedFunctionToolError(message string) error {
	switch message {
	case "invocation identity or scope denied":
		return &gatewayFailure{"invocation_denied", "Function identity or scope denied", 403, nil}
	case "authenticated event must not contain session credentials":
		return &gatewayFailure{"invalid_request", "Authenticated Function input contains credential fields", 400, nil}
	case "context deadline exceeded":
		return &gatewayFailure{"upstream_timeout", "Authenticated Function invocation timed out", 504, nil}
	default:
		return errors.New("authenticated Function invocation rejected")
	}
}
