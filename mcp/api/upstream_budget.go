package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
)

var errGatewayDeadline = errors.New("gateway route budget exhausted")

type gatewayRequestIDKey struct{}

func gatewayRequestID(ctx context.Context) string {
	id, _ := ctx.Value(gatewayRequestIDKey{}).(string)
	return id
}

type gatewayFailure struct {
	code, message string
	status        int
	cause         error
}

func (e *gatewayFailure) Error() string { return "[" + e.code + "] " + e.message }
func (e *gatewayFailure) Unwrap() error { return e.cause }

func classifyGatewayFailure(ctx context.Context, err error) *gatewayFailure {
	if errors.Is(context.Cause(ctx), errGatewayDeadline) {
		return &gatewayFailure{"gateway_timeout", "Gateway route deadline exceeded", 504, err}
	}
	if ctx.Err() == context.Canceled {
		return &gatewayFailure{"client_cancelled", "Client disconnected", 499, err}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return &gatewayFailure{"upstream_timeout", "Incoming request deadline exceeded", 504, err}
	}
	var failure *gatewayFailure
	if errors.As(err, &failure) {
		return failure
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return &gatewayFailure{"upstream_timeout", "Upstream timed out", 504, err}
	}
	var size *http.MaxBytesError
	if errors.As(err, &size) {
		return &gatewayFailure{"request_too_large", "Request body too large", 413, err}
	}
	return &gatewayFailure{"upstream_error", "Upstream request failed", 502, err}
}

func writeGatewayFailure(w http.ResponseWriter, ctx context.Context, err error) (int, error) {
	f := classifyGatewayFailure(ctx, err)
	// A disconnected browser cannot receive a response. The caller records 499
	// and aborts the HTTP handler without attempting to write an error body.
	if f.status != 499 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": f.message, "error_code": f.code, "request_id": gatewayRequestID(ctx)})
	}
	return f.status, f
}

func functionDeadlineFailure(status int, body []byte) *gatewayFailure {
	if status < 400 {
		return nil
	}
	var payload struct {
		Code string `json:"error_code"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	switch payload.Code {
	case "queue_timeout":
		return &gatewayFailure{"queue_timeout", "Functions queue deadline exceeded", 504, nil}
	case "invocation_timeout", "upstream_timeout", "app_call_timeout", "integration_timeout":
		return &gatewayFailure{"upstream_timeout", "Upstream execution deadline exceeded", 504, nil}
	}
	return nil
}
