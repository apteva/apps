package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type storageUploadError struct {
	Code, Phase, RetryAfter, Body string
	Status, Attempts              int
}

func (e *storageUploadError) Error() string {
	message := fmt.Sprintf("%s: phase=%s http_status=%d attempts=%d retry_after=%q body=%s", e.Code, e.Phase, e.Status, e.Attempts, e.RetryAfter, e.Body)
	if e.Code == "storage_upload_quota_exhausted" {
		message += "; free/complete pending uploads or increase the Storage quota before retrying"
	}
	return message
}
func newStorageUploadError(method, path string, status int, retryAfter, body string, attempts int) *storageUploadError {
	phase := "multipart_init"
	if strings.Contains(path, "/parts/") {
		phase = "part_upload"
	} else if strings.Contains(path, "/complete") {
		phase = "complete"
	} else if method == http.MethodDelete {
		phase = "abort"
	}
	code := "storage_upload_failed"
	text := strings.ToLower(body)
	if status == 429 {
		code = "storage_upload_rate_limited"
		if strings.Contains(text, "quota") || strings.Contains(text, "pending") {
			code = "storage_upload_quota_exhausted"
		}
	}
	return &storageUploadError{Code: code, Phase: phase, Status: status, RetryAfter: retryAfter, Body: truncateRenderFailure(body, 1000), Attempts: attempts}
}
func storageRetryDelay(header string, attempt int) time.Duration {
	delay := time.Second * time.Duration(1<<(attempt-1))
	seconds, err := strconv.Atoi(header)
	if err == nil && seconds > 0 {
		if retry := time.Duration(seconds) * time.Second; retry > delay {
			delay = retry
		}
	} else if when, err := http.ParseTime(header); err == nil {
		if retry := time.Until(when); retry > delay {
			delay = retry
		}
	}
	if delay > 10*time.Second {
		return 10 * time.Second
	}
	return delay
}
