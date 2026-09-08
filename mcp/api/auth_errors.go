package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type authorizationError struct {
	status  int
	message string
	cause   error
}

func (e *authorizationError) Error() string { return e.message }
func (e *authorizationError) Unwrap() error { return e.cause }
func authFailure(status int, message string, cause error) error {
	return &authorizationError{status, message, cause}
}
func authBackendFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return authFailure(503, "authentication request canceled", err)
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return authFailure(504, "authentication service timed out", err)
	}
	return authFailure(502, "authentication service unavailable", err)
}
func (a *App) writeAuthorizationError(w http.ResponseWriter, r *http.Request, err error, row *RequestLog, start time.Time) {
	status, message := http.StatusUnauthorized, "authentication rejected"
	var failure *authorizationError
	if errors.As(err, &failure) {
		status, message = failure.status, failure.message
	}
	if r.Context().Err() != nil {
		status = 499
		message = "client request canceled"
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			status = 504
			message = "request timed out"
		}
	}
	row.StatusCode = status
	row.Error = message
	deadline, _ := r.Context().Deadline()
	connection := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(r.Context().Value(http.LocalAddrContextKey))+"|"+r.RemoteAddr)))[:16]
	cause := err
	if failure != nil && failure.cause != nil {
		cause = failure.cause
	}
	if a.ctx != nil {
		a.ctx.Logger().Warn("gateway authorization failed", "request_id", row.RequestID, "connection_key", connection, "status", status, "parent_context", fmt.Sprint(r.Context().Err()), "parent_deadline", deadline, "elapsed_ms", time.Since(start).Milliseconds(), "cause", safeUpstreamError(cause))
	}
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	httpErr(w, status, message)
}
