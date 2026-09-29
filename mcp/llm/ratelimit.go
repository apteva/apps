package main

import (
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Monthly caps protect spend but do nothing about burst: a caller in a retry
// loop can saturate a shared upstream account while staying inside budget.
// These limits are per-token and short-window, and are enforced before any
// provider work is scheduled.
//
// State is in-process. The gateway runs one instance per project against a
// local SQLite file, so a single process sees every request for a token. If the
// gateway is ever replicated, this must move to shared storage - limits would
// otherwise be per-replica.
type rateLimiter struct {
	mu       sync.Mutex
	windows  map[string]*rateWindow
	inflight map[int64]int64
	now      func() time.Time
}

type rateWindow struct {
	start time.Time
	count int64
}

const rateWindowSize = time.Minute

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		windows:  map[string]*rateWindow{},
		inflight: map[int64]int64{},
		now:      time.Now,
	}
}

var gatewayLimiter = newRateLimiter()

// rateLimitError carries the wait a caller should honour before retrying.
type rateLimitError struct {
	message    string
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string { return e.message }

func (l *rateLimiter) windowKey(tokenID int64, bucket string) string {
	return strconv.FormatInt(tokenID, 10) + "\x00" + bucket
}

// reserveWindow adds cost to a named per-token window, rejecting the request if
// it would exceed limit. A limit of zero means unlimited, which keeps existing
// internal callers unaffected.
func (l *rateLimiter) reserveWindow(tokenID int64, bucket string, cost, limit int64) error {
	if limit <= 0 || cost < 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := l.windowKey(tokenID, bucket)
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= rateWindowSize {
		w = &rateWindow{start: now}
		l.windows[key] = w
	}
	if w.count+cost > limit {
		retry := rateWindowSize - now.Sub(w.start)
		if retry < time.Second {
			retry = time.Second
		}
		return &rateLimitError{message: bucket + " rate limit exceeded", retryAfter: retry}
	}
	w.count += cost
	return nil
}

// acquireSlot takes an in-flight slot, released by releaseSlot in a defer.
func (l *rateLimiter) acquireSlot(tokenID, limit int64) error {
	if limit <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[tokenID] >= limit {
		return &rateLimitError{message: "concurrent request limit exceeded", retryAfter: time.Second}
	}
	l.inflight[tokenID]++
	return nil
}

func (l *rateLimiter) releaseSlot(tokenID, limit int64) {
	if limit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[tokenID] > 0 {
		l.inflight[tokenID]--
	}
}

// gc drops windows that have fully expired, so a long-lived process does not
// retain state for tokens that stopped calling.
func (l *rateLimiter) gc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, w := range l.windows {
		if now.Sub(w.start) >= 2*rateWindowSize {
			delete(l.windows, key)
		}
	}
	for id, n := range l.inflight {
		if n <= 0 {
			delete(l.inflight, id)
		}
	}
}

// tightestRateLimits folds the project and subject policies into the strictest
// limit that applies, matching how monthly caps already compose.
func tightestRateLimits(policies []*Policy) (requests, tokens, concurrent int64) {
	for _, pol := range policies {
		if pol == nil {
			continue
		}
		requests = minPositive(requests, pol.Limits.RequestsPerMinute)
		tokens = minPositive(tokens, pol.Limits.TokensPerMinute)
		concurrent = minPositive(concurrent, pol.Limits.MaxConcurrentRequests)
	}
	return requests, tokens, concurrent
}

func writeRateLimitError(w http.ResponseWriter, err *rateLimitError) {
	seconds := int64(err.retryAfter / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", err.Error())
}

// emitRateLimitEvent reuses the existing policy-limit signal so operators see
// burst rejections on the same stream as budget rejections.
func emitRateLimitEvent(ctx *sdk.AppCtx, ident *TokenIdentity, limit string, err *rateLimitError) {
	if ctx == nil || ident == nil {
		return
	}
	ctx.EmitWithProject("llm.policy.limit_exceeded", ident.ProjectID, map[string]any{
		"limit":        limit,
		"subject_type": ident.SubjectType,
		"subject_id":   ident.SubjectID,
		"token_id":     ident.ID,
		"retry_after":  int64(err.retryAfter / time.Second),
		"error":        err.Error(),
	})
}
