package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	registrationWindow      = time.Minute
	registrationGlobalLimit = 600
	registrationSourceLimit = 30
	registrationPeerLimit   = 240
	registrationTokenLimit  = 6
)

type registerRateLimiter struct {
	mu        sync.Mutex
	events    map[string][]time.Time
	now       func() time.Time
	lastSweep time.Time
}

type rateLimitScope struct {
	key   string
	limit int
}

func newRegisterRateLimiter() *registerRateLimiter {
	return &registerRateLimiter{
		events: make(map[string][]time.Time),
		now:    time.Now,
	}
}

func (l *registerRateLimiter) allow(key string, limit int, window time.Duration) bool {
	if l == nil || key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-window)
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= window {
		for candidate, events := range l.events {
			recent := trimRateEvents(events, cutoff)
			if len(recent) == 0 {
				delete(l.events, candidate)
			} else {
				l.events[candidate] = recent
			}
		}
		l.lastSweep = now
	}

	recent := trimRateEvents(l.events[key], cutoff)
	if len(recent) >= limit {
		l.events[key] = recent
		return false
	}
	l.events[key] = append(recent, now)
	return true
}

func trimRateEvents(events []time.Time, cutoff time.Time) []time.Time {
	first := 0
	for first < len(events) && !events[first].After(cutoff) {
		first++
	}
	return events[first:]
}

func (a *App) allowRegistration(w http.ResponseWriter, r *http.Request, tokenHash string) bool {
	if a.limiter == nil {
		a.limiter = newRegisterRateLimiter()
	}
	var scopes []rateLimitScope
	if tokenHash == "" {
		source, peer := registrationSources(r)
		scopes = []rateLimitScope{
			{key: "global", limit: registrationGlobalLimit},
			{key: "source:" + source, limit: registrationSourceLimit},
			{key: "peer:" + peer, limit: registrationPeerLimit},
		}
	} else {
		scopes = []rateLimitScope{
			{key: "token:" + tokenHash, limit: registrationTokenLimit},
		}
	}
	for _, scope := range scopes {
		if !a.limiter.allow(scope.key, scope.limit, registrationWindow) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "device registration rate limit exceeded")
			return false
		}
	}
	return true
}

func registrationSources(r *http.Request) (source, peer string) {
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	clean := make([]string, 0, len(forwarded))
	for _, value := range forwarded {
		value = strings.TrimSpace(value)
		if net.ParseIP(value) != nil {
			clean = append(clean, value)
		}
	}
	if len(clean) > 0 {
		return clean[0], clean[len(clean)-1]
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if net.ParseIP(host) == nil {
		host = "unknown"
	}
	return host, host
}
