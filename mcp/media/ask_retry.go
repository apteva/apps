package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxAskAttempts = 3
const maxAskRetryDelay = 10 * time.Second

var askHTTPStatusPattern = regexp.MustCompile(`(?i)(?:http\s+|status\s*[=:]?\s*)([45]\d{2})\b`)
var askStreamTransportPattern = regexp.MustCompile(`(?i)\bstream error:\s*stream ID \d+;\s*(?:INTERNAL_ERROR|REFUSED_STREAM)\b`)

type askAttempt struct {
	Attempt   int                   `json:"attempt"`
	Status    int                   `json:"status,omitempty"`
	Code      string                `json:"code"`
	Retryable bool                  `json:"retryable"`
	Error     string                `json:"upstream_error,omitempty"`
	RetryInfo *descriptionRetryInfo `json:"retry_info,omitempty"`
}
type askRetryDiagnostics struct {
	Attempts    int          `json:"attempts"`
	MaxAttempts int          `json:"max_attempts"`
	ElapsedMs   int64        `json:"elapsed_ms"`
	History     []askAttempt `json:"history"`
	StopReason  string       `json:"stop_reason,omitempty"`
}
type askIntegrationError struct {
	Diagnostics askRetryDiagnostics
	Cause       error
}

func (e *askIntegrationError) Error() string {
	raw, _ := json.Marshal(e.Diagnostics)
	return fmt.Sprintf("media_ask integration failed after %d attempt(s) (%s): %v; request_diagnostics=%s", e.Diagnostics.Attempts, e.Diagnostics.StopReason, e.Cause, raw)
}
func (e *askIntegrationError) Unwrap() error { return e.Cause }

// The whole retry sequence owns an ask slot, including backoff. The existing
// per-call slot remains held until an actual legacy upstream call has ended,
// even if its caller already timed out. Never overlap a timed-out request.
func executeAskIntegrationWithRetry(app *sdk.AppCtx, conn int64, tool string, input map[string]any, timeout time.Duration) (*sdk.ExecuteResult, askRetryDiagnostics, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	key := "media_ask_retry:" + strconv.FormatInt(conn, 10) + ":" + tool
	if _, busy := integrationCallsInFlight.LoadOrStore(key, struct{}{}); busy {
		d := askRetryDiagnostics{MaxAttempts: maxAskAttempts, StopReason: "busy"}
		return nil, d, &askIntegrationError{Diagnostics: d, Cause: errors.New("previous media_ask request is still running")}
	}
	defer integrationCallsInFlight.Delete(key)
	return runAskIntegrationAttempts(ctx, func(ctx context.Context) (*sdk.ExecuteResult, error) {
		deadline, _ := ctx.Deadline()
		return executeIntegrationToolContext(ctx, app, "media_ask", conn, tool, input, time.Until(deadline))
	}, func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-mediaDone(app):
			return errors.New("integration call cancelled: app shutting down")
		}
	})
}

func runAskIntegrationAttempts(ctx context.Context, call func(context.Context) (*sdk.ExecuteResult, error), wait func(context.Context, time.Duration) error) (*sdk.ExecuteResult, askRetryDiagnostics, error) {
	start := time.Now()
	d := askRetryDiagnostics{MaxAttempts: maxAskAttempts, History: []askAttempt{}}
	fail := func(reason string, cause error) (*sdk.ExecuteResult, askRetryDiagnostics, error) {
		d.StopReason = reason
		d.ElapsedMs = time.Since(start).Milliseconds()
		return nil, d, &askIntegrationError{Diagnostics: d, Cause: cause}
	}
	var last error
	for attempt := 1; attempt <= maxAskAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fail("deadline_or_cancelled", errors.Join(last, err))
		}
		res, err := call(ctx)
		d.Attempts = attempt
		if err == nil && res != nil && res.Success {
			d.History = append(d.History, askAttempt{Attempt: attempt, Status: res.Status, Code: "success"})
			d.ElapsedMs = time.Since(start).Milliseconds()
			return res, d, nil
		}
		entry, until := classifyAskFailure(res, err)
		entry.Attempt = attempt
		d.History = append(d.History, entry)
		last = err
		if last == nil {
			last = errors.New(entry.Error)
		}
		if ctx.Err() != nil {
			return fail("deadline_or_cancelled", errors.Join(last, ctx.Err()))
		}
		if !entry.Retryable {
			return fail(entry.Code, last)
		}
		if attempt == maxAskAttempts {
			return fail("attempts_exhausted", last)
		}
		delay := time.Second * time.Duration(1<<(attempt-1))
		if upstream := time.Until(until); upstream > delay {
			delay = upstream
		}
		// Never shorten a provider reset to squeeze another call into this request.
		if delay > maxAskRetryDelay {
			return fail("upstream_retry_deferred", last)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return fail("insufficient_retry_budget", last)
		}
		if err := wait(ctx, delay); err != nil {
			return fail("retry_interrupted", errors.Join(last, err))
		}
	}
	return fail("attempts_exhausted", last)
}

func classifyAskFailure(res *sdk.ExecuteResult, err error) (askAttempt, time.Time) {
	entry := askAttempt{Code: "upstream_error"}
	if res != nil {
		entry.Status = res.Status
		entry.Error = string(res.Data)
	}
	if err != nil {
		if entry.Error != "" {
			entry.Error = err.Error() + "; " + entry.Error
		} else {
			entry.Error = err.Error()
		}
	}
	if entry.Error == "" {
		entry.Error = "integration returned no successful response"
	}
	text := strings.ToLower(entry.Error)
	entry.Error = truncateRenderFailure(entry.Error, 2000)
	// Inspect details before HTTP status: gateways can wrap auth/quota errors in 500/200.
	for _, marker := range []string{"insufficient_quota", "quota_exceeded", "usage_limit_reached", "quota exhausted", "quota exceeded", "billing_hard_limit", "payment_required", "insufficient credits", "credits exhausted", "credit balance"} {
		if strings.Contains(text, marker) {
			entry.Code = "quota_or_billing"
			return entry, time.Time{}
		}
	}
	for _, marker := range []string{"invalid_api_key", "authentication_error", "unauthorized", "permission_denied", "missing access_token", "invalid access token", "token expired", "invalid_grant"} {
		if strings.Contains(text, marker) {
			entry.Code = "authentication"
			return entry, time.Time{}
		}
	}
	for _, marker := range []string{"invalid_request_error", "invalid_image", "unsupported_image", "invalid image", "unsupported model", "model_not_found", "context_length_exceeded", "max_output_tokens", "previous integration call is still running", "app shutting down"} {
		if strings.Contains(text, marker) {
			entry.Code = "invalid_request_or_unavailable"
			return entry, time.Time{}
		}
	}
	for _, match := range askHTTPStatusPattern.FindAllStringSubmatch(text, -1) {
		status, _ := strconv.Atoi(match[1])
		if entry.Status == 0 {
			entry.Status = status
		}
		switch status {
		case 400, 401, 402, 403, 404, 405, 413, 415, 422:
			entry.Code = "permanent_http_error"
			return entry, time.Time{}
		}
	}
	switch entry.Status {
	case 400, 401, 402, 403, 404, 405, 413, 415, 422:
		entry.Code = "permanent_http_error"
		return entry, time.Time{}
	}
	if info, until, limited := descriptionRateLimitInfo(res, err, time.Now()); limited {
		entry.Code = "rate_limited"
		entry.Retryable = true
		if until.After(time.Now()) {
			info.NextAttemptAt = until.UTC().Format(time.RFC3339)
		}
		entry.RetryInfo = &info
		return entry, until
	}
	switch entry.Status {
	case 408, 425, 500, 502, 503, 504:
		entry.Code = "transient_http_error"
		entry.Retryable = true
		return entry, time.Time{}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		entry.Code = "cancelled_or_timed_out"
		return entry, time.Time{}
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		entry.Code = "transport_error"
		entry.Retryable = true
		return entry, time.Time{}
	}
	// The integration adapter can flatten HTTP/2 stream failures into text,
	// losing their net.Error identity even though the response began with 200.
	if askStreamTransportPattern.MatchString(text) {
		entry.Code = "transport_error"
		entry.Retryable = true
		return entry, time.Time{}
	}
	if res != nil && transientAskProviderError(res.Data) {
		entry.Code = "transient_upstream_error"
		entry.Retryable = true
		return entry, time.Time{}
	}
	for _, marker := range []string{"codex stream ended with error", "codex stream ended with response.failed", "codex stream ended without response.completed", "connection reset by peer", "broken pipe", "unexpected eof", "connection refused", "tls handshake timeout", "connection timed out"} {
		if strings.Contains(text, marker) {
			entry.Code = "transport_error"
			if strings.Contains(marker, "codex stream") {
				entry.Code = "stream_failure_unknown"
			}
			entry.Retryable = true
			return entry, time.Time{}
		}
	}
	return entry, time.Time{}
}

// Look only at structured error envelopes: a code mentioned in output text or
// unrelated metadata must not turn an otherwise unknown failure into a retry.
func transientAskProviderError(raw []byte) bool {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	var visit func(any, bool, int) bool
	visit = func(value any, inError bool, depth int) bool {
		if depth > 12 {
			return false
		}
		switch v := value.(type) {
		case map[string]any:
			if v["type"] == "error" {
				inError = true // Codex/Responses stream error event.
			}
			for key, child := range v {
				if inError && (key == "code" || key == "type") {
					code, _ := child.(string)
					switch strings.ToLower(code) {
					case "internal_error", "server_error", "service_unavailable", "overloaded_error":
						return true
					}
				}
				if visit(child, inError || key == "error", depth+1) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if visit(child, inError, depth+1) {
					return true
				}
			}
		case string:
			v = strings.TrimSpace(v)
			if inError && len(v) > 0 && v[0] == '{' {
				var nested any
				if json.Unmarshal([]byte(v), &nested) == nil {
					return visit(nested, true, depth+1)
				}
			}
		}
		return false
	}
	return visit(root, false, 0)
}
