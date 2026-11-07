package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	publicHTTPAttempts = 3
	publicRetryBase    = 200 * time.Millisecond
	publicRetryCap     = 2 * time.Second
)

// publicGET is shared by every direct public market-data client. It retries
// transient network failures, rate limits, and 5xx responses, but fails fast
// on permanent 4xx responses. Response bodies are bounded on every attempt.
func publicGET(parent context.Context, client *http.Client, provider, url string, maxBytes int64, headers map[string]string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < publicHTTPAttempts; attempt++ {
		if err := parent.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(parent, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode/100 == 2 {
				return body, nil
			}
			if readErr != nil {
				lastErr = fmt.Errorf("%s response read: %w", provider, readErr)
			} else {
				lastErr = fmt.Errorf("%s HTTP %d: %s", provider, resp.StatusCode, strings.TrimSpace(string(body)))
				if !retryableHTTPStatus(resp.StatusCode) {
					return nil, lastErr
				}
			}
			if attempt+1 < publicHTTPAttempts {
				delay := retryDelay(resp.Header.Get("Retry-After"), attempt, time.Now())
				if err := waitForRetry(parent, delay); err != nil {
					return nil, err
				}
			}
			continue
		}
		lastErr = fmt.Errorf("%s request: %w", provider, err)
		if attempt+1 < publicHTTPAttempts {
			if err := waitForRetry(parent, retryDelay("", attempt, time.Now())); err != nil {
				return nil, err
			}
		}
	}
	return nil, lastErr
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= 500
}

func retryDelay(retryAfter string, attempt int, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > publicRetryCap {
			return publicRetryCap
		}
		return delay
	}
	if at, err := http.ParseTime(strings.TrimSpace(retryAfter)); err == nil {
		delay := at.Sub(now)
		if delay < 0 {
			return 0
		}
		if delay > publicRetryCap {
			return publicRetryCap
		}
		return delay
	}
	delay := publicRetryBase * time.Duration(1<<attempt)
	if delay > publicRetryCap {
		return publicRetryCap
	}
	return delay
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
