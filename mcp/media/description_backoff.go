package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type descriptionRetryInfo struct {
	Status        int               `json:"status"`
	RetryAfter    string            `json:"retry_after,omitempty"`
	Reset         map[string]string `json:"reset,omitempty"`
	NextAttemptAt string            `json:"next_attempt_at,omitempty"`
}

var description429Pattern = regexp.MustCompile(`(?i)(?:http\s+|status\s*[=:]?\s*)429\b|rate_limit_exceeded|usage_limit_reached|too many requests`)

// Keep only retry/reset metadata, never credentials or unrelated headers.
func descriptionRateLimitInfo(res *sdk.ExecuteResult, callErr error, now time.Time) (descriptionRetryInfo, time.Time, bool) {
	info := descriptionRetryInfo{Status: 429, Reset: map[string]string{}}
	limited := res != nil && res.Status == 429
	var data json.RawMessage
	if res != nil {
		data = res.Data
	}
	if callErr != nil {
		limited = limited || description429Pattern.MatchString(callErr.Error())
		// Older gateways wrap an upstream JSON error in their error string.
		// Recover explicit reset hints when available instead of discarding them.
		message := callErr.Error()
		if start := strings.IndexByte(message, '{'); start >= 0 && json.Valid([]byte(message[start:])) {
			data = json.RawMessage(message[start:])
		}
	}
	if res != nil {
		for k, v := range res.Headers {
			key := strings.ToLower(k)
			switch key {
			case "retry-after":
				info.RetryAfter = v
			case "x-ratelimit-reset", "x-ratelimit-reset-requests", "x-ratelimit-reset-tokens", "ratelimit-reset":
				info.Reset[key] = v
			}
		}
	}
	var body map[string]any
	if json.Unmarshal(data, &body) == nil {
		if nested, ok := body["error"].(map[string]any); ok {
			body = nested
		}
		for _, k := range []string{"type", "code", "message"} {
			limited = limited || description429Pattern.MatchString(fmt.Sprint(body[k]))
		}
		for _, k := range []string{"retry_after", "retry_after_seconds"} {
			if v, ok := body[k]; ok && info.RetryAfter == "" {
				info.RetryAfter = fmt.Sprint(v)
			}
		}
		for _, k := range []string{"reset_at", "resets_at", "reset_at_unix"} {
			if v, ok := body[k]; ok {
				info.Reset[k] = fmt.Sprint(v)
			}
		}
	}
	var until time.Time
	if t := parseDescriptionRetryTime(info.RetryAfter, now, false); t.After(until) {
		until = t
	}
	for k, v := range info.Reset {
		absolute := k == "x-ratelimit-reset" || strings.Contains(k, "_at")
		if t := parseDescriptionRetryTime(v, now, absolute); t.After(until) {
			until = t
		}
	}
	return info, until, limited
}

func parseDescriptionRetryTime(value string, now time.Time, absolute bool) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if t, err := http.ParseTime(value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	if n, err := strconv.ParseFloat(value, 64); err == nil && n >= 0 && n < 1e12 {
		if absolute {
			return time.Unix(int64(n), 0)
		}
		// Guard malformed unbounded durations without shortening valid resets.
		if n <= float64((365*24*time.Hour)/time.Second) {
			return now.Add(time.Duration(n * float64(time.Second)))
		}
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return now.Add(d)
	}
	return time.Time{}
}

func descriptionBackoffActive(db *sql.DB, connection int64, tool, model string, now time.Time) (bool, error) {
	var next string
	err := db.QueryRow(`SELECT next_attempt_at FROM description_backoff WHERE connection_id=? AND tool=? AND model=?`, connection, tool, model).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, err := time.Parse(time.RFC3339, next)
	return t.After(now), err
}

// Retry on a later sweep rather than sleeping inside the worker. Exponential
// fallback is capped at one hour; explicit upstream retry/reset times can defer
// longer. No busy loop or extra provider requests are introduced.
func recordDescriptionBackoff(db *sql.DB, connection int64, tool, model string, info descriptionRetryInfo, upstream, now time.Time, cooldown int) (descriptionRetryInfo, error) {
	tx, err := db.Begin()
	if err != nil {
		return info, err
	}
	defer tx.Rollback()
	var attempts int
	err = tx.QueryRow(`INSERT INTO description_backoff(connection_id,tool,model,attempts,next_attempt_at)
		VALUES(?,?,?,1,?) ON CONFLICT(connection_id,tool,model) DO UPDATE SET attempts=MIN(attempts+1,32)
		RETURNING attempts`, connection, tool, model, now.UTC().Format(time.RFC3339)).Scan(&attempts)
	if err != nil {
		return info, err
	}
	if cooldown < 1 {
		cooldown = 1
	}
	if cooldown > 3600 {
		cooldown = 3600
	}
	delay := time.Duration(cooldown) * time.Second
	for i := 1; i < attempts && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	next := now.Add(delay)
	if upstream.After(next) {
		next = upstream
	}
	// Round up so truncating RFC3339 seconds never retries before the hint.
	info.NextAttemptAt = next.UTC().Add(time.Second - time.Nanosecond).Truncate(time.Second).Format(time.RFC3339)
	raw, _ := json.Marshal(info)
	_, err = tx.Exec(`UPDATE description_backoff SET next_attempt_at=?,retry_info=? WHERE connection_id=? AND tool=? AND model=?`, info.NextAttemptAt, string(raw), connection, tool, model)
	if err != nil {
		return info, err
	}
	return info, tx.Commit()
}
