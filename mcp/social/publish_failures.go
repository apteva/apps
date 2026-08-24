package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type upstreamCallError struct {
	Status         int
	Body           string
	ExistingPostID string
}

func (e *upstreamCallError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	return fmt.Sprintf("upstream %d: %s", e.Status, body)
}

type publishFailure struct {
	Code           string
	Retryable      bool
	UpstreamStatus int
	ExistingPostID string
}

func classifyPublishFailure(err error) publishFailure {
	failure := publishFailure{Code: "upstream_error", Retryable: true}
	var upstream *upstreamCallError
	if errors.As(err, &upstream) {
		failure.UpstreamStatus = upstream.Status
		failure.ExistingPostID = upstream.ExistingPostID
		lower := strings.ToLower(upstream.Body)
		if upstream.Status == 409 && (upstream.ExistingPostID != "" || strings.Contains(lower, "duplicate")) {
			failure.Code = "duplicate_content"
			failure.Retryable = false
			return failure
		}
		if upstream.Status == 429 || upstream.Status >= 500 || upstream.Status == 0 {
			return failure
		}
		if upstream.Status >= 400 && upstream.Status < 500 {
			failure.Code = "upstream_rejected"
			failure.Retryable = false
		}
		return failure
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "duplicate") && (strings.Contains(lower, "409") || strings.Contains(lower, "existingpost")) {
		failure.Code = "duplicate_content"
		failure.Retryable = false
		if start := strings.Index(lower, "existingpostid"); start >= 0 {
			tail := strings.TrimLeft(err.Error()[start+len("existingPostId"):], " \":='")
			if end := strings.IndexAny(tail, "\"',} ]"); end >= 0 {
				tail = tail[:end]
			}
			failure.ExistingPostID = strings.TrimSpace(tail)
		}
		return failure
	}
	for _, fragment := range []string{
		"unsupported platform", "requires at least", "must be ", "invalid ",
		"too long", "exceeds ", "expected an ", "missing ", "not active",
	} {
		if strings.Contains(lower, fragment) {
			failure.Code = "validation_error"
			failure.Retryable = false
			break
		}
	}
	return failure
}

func extractExistingPostID(raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var walk func(any) string
	walk = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range []string{"existingPostId", "existing_post_id", "existingPostID"} {
				if id := strings.TrimSpace(toString(typed[key])); id != "" {
					return id
				}
			}
			for _, child := range typed {
				if id := walk(child); id != "" {
					return id
				}
			}
		case []any:
			for _, child := range typed {
				if id := walk(child); id != "" {
					return id
				}
			}
		}
		return ""
	}
	return walk(value)
}
