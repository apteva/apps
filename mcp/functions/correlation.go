package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
)

type correlationKey struct{}

var correlationPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func withCorrelation(r *http.Request, w http.ResponseWriter) *http.Request {
	id := r.Header.Get("X-Request-ID")
	if !correlationPattern.MatchString(id) {
		return r
	}
	w.Header().Set("X-Request-ID", id)
	return r.WithContext(context.WithValue(r.Context(), correlationKey{}, id))
}

// Preserve existing error_code values for callers while returning HTTP 504 for
// actual deadline failures. Admission rejection without waiting stays HTTP 503.
func writeInvocationDeadline(w http.ResponseWriter, r *http.Request, res *invokeResult, err error) bool {
	code := deadlineErrorCode(err)
	if res != nil && res.ErrorCode != "" {
		code = res.ErrorCode
	}
	message := "Upstream execution deadline exceeded"
	switch code {
	case "queue_timeout":
		message = "Functions queue deadline exceeded"
	case "invocation_timeout", "upstream_timeout", "app_call_timeout", "integration_timeout":
	default:
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGatewayTimeout)
	body := map[string]any{"error": message, "error_code": code, "request_id": correlationID(r.Context())}
	if res != nil {
		body["invocation_id"] = res.InvocationID
		body["resources"] = res.Resources
	}
	_ = json.NewEncoder(w).Encode(body)
	return true
}

func deadlineErrorCode(err error) string {
	var resource *ResourceError
	if errors.As(err, &resource) && resource.QueueTimedOut {
		return "queue_timeout"
	}
	return errorCode(err)
}
