package main

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Per-minute request and token limits. Counters live in memory: they reset
// when the sidecar restarts and assume a single llm sidecar, which is how the
// platform runs apps today. Each key keeps a sliding 60-second window of
// one-second buckets.

const rateWindowSeconds = 60

type rateBucket struct {
	sec      int64
	requests int64
	tokens   int64
}

type rateWindow struct {
	buckets   [rateWindowSeconds]rateBucket
	lastTouch int64
}

func (w *rateWindow) bucket(sec int64) *rateBucket {
	b := &w.buckets[sec%rateWindowSeconds]
	if b.sec != sec {
		*b = rateBucket{sec: sec}
	}
	return b
}

func (w *rateWindow) totals(now int64) (requests, tokens, oldest int64) {
	oldest = now
	for i := range w.buckets {
		b := w.buckets[i]
		if b.sec <= now-rateWindowSeconds || b.sec > now {
			continue
		}
		if b.requests == 0 && b.tokens == 0 {
			continue
		}
		requests += b.requests
		tokens += b.tokens
		if b.sec < oldest {
			oldest = b.sec
		}
	}
	return requests, tokens, oldest
}

type rateRule struct {
	Key   string
	Scope string
	RPM   int64
	TPM   int64
}

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
	calls   int64
	now     func() time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: map[string]*rateWindow{}, now: time.Now}
}

type rateCharge struct {
	limiter *rateLimiter
	keys    []string
	sec     int64
	tokens  int64
	done    bool
}

// rateStatus describes the tightest limit for response headers.
type rateStatus struct {
	LimitRequests     int64
	RemainingRequests int64
	ResetRequests     int64
	LimitTokens       int64
	RemainingTokens   int64
	ResetTokens       int64
}

func rateRulesFor(ident *TokenIdentity, policies []*Policy) []rateRule {
	var rules []rateRule
	if ident == nil {
		return nil
	}
	if len(policies) > 0 && policies[0] != nil {
		l := policies[0].Limits
		if l.RequestsPerMinute > 0 || l.TokensPerMinute > 0 {
			rules = append(rules, rateRule{Key: "p\x00" + ident.ProjectID, Scope: "project", RPM: l.RequestsPerMinute, TPM: l.TokensPerMinute})
		}
	}
	if len(policies) > 1 && policies[1] != nil {
		l := policies[1].Limits
		if l.RequestsPerMinute > 0 || l.TokensPerMinute > 0 {
			rules = append(rules, rateRule{Key: "s\x00" + ident.ProjectID + "\x00" + ident.SubjectType + "\x00" + ident.SubjectID, Scope: "subject", RPM: l.RequestsPerMinute, TPM: l.TokensPerMinute})
		}
	}
	if ident.ID > 0 {
		var rpm, tpm int64
		for _, pol := range policies {
			if pol == nil {
				continue
			}
			rpm = minPositive(rpm, pol.Limits.TokenRequestsPerMinute)
			tpm = minPositive(tpm, pol.Limits.TokenTokensPerMinute)
		}
		if rpm > 0 || tpm > 0 {
			rules = append(rules, rateRule{Key: "t\x00" + strconv.FormatInt(ident.ID, 10), Scope: "token", RPM: rpm, TPM: tpm})
		}
	}
	return rules
}

// Charge admits one request costing `tokens` against every rule, or returns
// a 429 without charging anything.
func (l *rateLimiter) Charge(rules []rateRule, tokens int64) (*rateCharge, *rateStatus, error) {
	if l == nil || len(rules) == 0 {
		return nil, nil, nil
	}
	if tokens < 0 {
		tokens = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().Unix()
	l.calls++
	if l.calls%1000 == 0 {
		l.gc(now)
	}
	status := &rateStatus{RemainingRequests: -1, RemainingTokens: -1}
	for _, rule := range rules {
		w := l.windows[rule.Key]
		var reqs, toks, oldest int64
		oldest = now
		if w != nil {
			reqs, toks, oldest = w.totals(now)
		}
		reset := oldest + rateWindowSeconds - now
		if reset < 1 {
			reset = 1
		}
		if rule.RPM > 0 && reqs+1 > rule.RPM {
			return nil, nil, rateLimitError(rule, "requests", rule.RPM, reset)
		}
		if rule.TPM > 0 && tokens > rule.TPM {
			return nil, nil, typedErr{status: http.StatusTooManyRequests, typ: "rate_limit_exceeded",
				msg: fmt.Sprintf("request needs %d tokens, more than the %s limit of %d tokens per minute", tokens, rule.Scope, rule.TPM)}
		}
		if rule.TPM > 0 && toks+tokens > rule.TPM {
			return nil, nil, rateLimitError(rule, "tokens", rule.TPM, reset)
		}
		if rule.RPM > 0 {
			remaining := rule.RPM - reqs - 1
			if status.RemainingRequests < 0 || remaining < status.RemainingRequests {
				status.LimitRequests, status.RemainingRequests, status.ResetRequests = rule.RPM, remaining, reset
			}
		}
		if rule.TPM > 0 {
			remaining := rule.TPM - toks - tokens
			if status.RemainingTokens < 0 || remaining < status.RemainingTokens {
				status.LimitTokens, status.RemainingTokens, status.ResetTokens = rule.TPM, remaining, reset
			}
		}
	}
	charge := &rateCharge{limiter: l, sec: now, tokens: tokens}
	for _, rule := range rules {
		w := l.windows[rule.Key]
		if w == nil {
			w = &rateWindow{}
			l.windows[rule.Key] = w
		}
		b := w.bucket(now)
		b.requests++
		b.tokens += tokens
		w.lastTouch = now
		charge.keys = append(charge.keys, rule.Key)
	}
	return charge, status, nil
}

// Settle replaces the estimated token charge with the actual count.
func (c *rateCharge) Settle(actualTokens int64) {
	c.apply(0, actualTokens-c.tokensOrZero())
}

// Release refunds a request that never reached a provider.
func (c *rateCharge) Release() {
	c.apply(-1, -c.tokensOrZero())
}

func (c *rateCharge) tokensOrZero() int64 {
	if c == nil {
		return 0
	}
	return c.tokens
}

func (c *rateCharge) apply(requests, tokens int64) {
	if c == nil || c.done || c.limiter == nil {
		return
	}
	c.done = true
	l := c.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now().Unix()-c.sec >= rateWindowSeconds {
		return
	}
	for _, key := range c.keys {
		w := l.windows[key]
		if w == nil {
			continue
		}
		b := &w.buckets[c.sec%rateWindowSeconds]
		if b.sec != c.sec {
			continue
		}
		b.requests += requests
		b.tokens += tokens
		if b.requests < 0 {
			b.requests = 0
		}
		if b.tokens < 0 {
			b.tokens = 0
		}
	}
}

func (l *rateLimiter) gc(now int64) {
	for key, w := range l.windows {
		if now-w.lastTouch > 2*rateWindowSeconds {
			delete(l.windows, key)
		}
	}
}

func rateLimitError(rule rateRule, unit string, limit, retryAfter int64) error {
	return typedErr{
		status: http.StatusTooManyRequests,
		typ:    "rate_limit_exceeded",
		msg:    fmt.Sprintf("%s rate limit reached: %d %s per minute", rule.Scope, limit, unit),
		headers: map[string]string{
			"Retry-After":                              strconv.FormatInt(retryAfter, 10),
			"X-Ratelimit-Limit-" + titleUnit(unit):     strconv.FormatInt(limit, 10),
			"X-Ratelimit-Remaining-" + titleUnit(unit): "0",
			"X-Ratelimit-Reset-" + titleUnit(unit):     strconv.FormatInt(retryAfter, 10) + "s",
		},
	}
}

func titleUnit(unit string) string {
	if unit == "tokens" {
		return "Tokens"
	}
	return "Requests"
}

func writeRateHeaders(w http.ResponseWriter, s *rateStatus) {
	if s == nil {
		return
	}
	if s.LimitRequests > 0 {
		w.Header().Set("X-Ratelimit-Limit-Requests", strconv.FormatInt(s.LimitRequests, 10))
		w.Header().Set("X-Ratelimit-Remaining-Requests", strconv.FormatInt(maxInt64(s.RemainingRequests, 0), 10))
		w.Header().Set("X-Ratelimit-Reset-Requests", strconv.FormatInt(s.ResetRequests, 10)+"s")
	}
	if s.LimitTokens > 0 {
		w.Header().Set("X-Ratelimit-Limit-Tokens", strconv.FormatInt(s.LimitTokens, 10))
		w.Header().Set("X-Ratelimit-Remaining-Tokens", strconv.FormatInt(maxInt64(s.RemainingTokens, 0), 10))
		w.Header().Set("X-Ratelimit-Reset-Tokens", strconv.FormatInt(s.ResetTokens, 10)+"s")
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
