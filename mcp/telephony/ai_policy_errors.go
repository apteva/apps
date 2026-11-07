package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The SDK currently returns text errors for thread events. Extract only safe,
// known categories and HTTP status; never retain response bodies, URLs or tokens.
var aiPolicyHTTPStatus = regexp.MustCompile(`(?i)\bhttp ([1-5][0-9]{2})\b`)

func aiPolicyControlFailure(err error) (class string, status int, detail string) {
	if err == nil {
		return "", 0, ""
	}
	text := err.Error()
	if match := aiPolicyHTTPStatus.FindStringSubmatch(text); len(match) == 2 {
		status, _ = strconv.Atoi(match[1])
	}
	switch {
	case errors.Is(err, context.Canceled):
		class = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		class = "timeout"
	case strings.Contains(text, "missing permission: platform.threads.write"):
		class = "missing_thread_permission"
	case strings.Contains(text, "thread event API unavailable"):
		class = "thread_api_unavailable"
	case status == 401 || status == 403:
		class = "authorization_rejected"
	case status == 404:
		class = "target_not_found"
	case status == 409:
		class = "target_conflict"
	case status == 429:
		class = "rate_limited"
	case status >= 500:
		class = "upstream_unavailable"
	case status >= 400:
		class = "request_rejected"
	default:
		var e net.Error
		if errors.As(err, &e) && e.Timeout() {
			class = "timeout"
		} else {
			class = "control_request_failed"
		}
	}
	detail = class
	if status != 0 {
		detail = fmt.Sprintf("%s (HTTP %d)", class, status)
	}
	return
}
func (s *aiPolicyState) recordControlError(now time.Time, kind string, err error) {
	class, status, detail := aiPolicyControlFailure(err)
	s.LastError = kind + ": " + detail
	s.event(now, kind, detail)
	e := &s.Events[len(s.Events)-1]
	e.ErrorClass, e.HTTPStatus = class, status
}
