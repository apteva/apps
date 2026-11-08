package main

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var errOverloaded = errors.New("MQTT publish capacity exhausted; retry with exponential backoff and jitter")
var errPayloadTooLarge = errors.New("MQTT payload exceeds the configured limit")

type scopeAdmission struct {
	mu          sync.Mutex
	scopes      map[string]*tokenBucket
	rate, burst int
}

func (a *App) initAdmission() error {
	for _, setting := range []struct {
		name          string
		def, min, max int
	}{
		{"max_clients", 1000, 1, 1000000}, {"max_payload_bytes", 1048576, 1024, 268435455},
		{"max_inflight_per_client", 128, 1, 65535}, {"max_pending_writes_per_client", 1024, 1, 1048576},
	} {
		if _, err := strictInt(a.ctx, setting.name, setting.def, setting.min, setting.max); err != nil {
			return err
		}
	}
	rate, err := strictInt(a.ctx, "max_publish_per_scope_per_second", 1000, 0, 1000000)
	if err != nil {
		return err
	}
	burst, err := strictInt(a.ctx, "max_publish_per_scope_burst", 2000, 1, 10000000)
	if err != nil {
		return err
	}
	if _, err := strictInt(a.ctx, "max_publish_per_second", 100, 0, 1000000); err != nil {
		return err
	}
	if _, err := strictInt(a.ctx, "max_publish_burst", 200, 1, 10000000); err != nil {
		return err
	}
	a.admission = &scopeAdmission{scopes: map[string]*tokenBucket{}, rate: rate, burst: burst}
	return nil
}

func (a *App) admitScope(scope string) error {
	if !validScope(scope) {
		return fmt.Errorf("ownership scope required for publish admission")
	}
	if a.admission == nil {
		return errors.New("MQTT publish admission is not initialized")
	}
	a.admission.mu.Lock()
	limiter := a.admission.scopes[scope]
	if limiter == nil {
		limiter = newTokenBucket(a.admission.rate, a.admission.burst)
		a.admission.scopes[scope] = limiter
	}
	a.admission.mu.Unlock()
	if !limiter.allow() {
		a.counters(scope).rateLimited.Add(1)
		a.rateLimitedMessages.Add(1)
		return errOverloaded
	}
	return nil
}

func publishHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, errPayloadTooLarge) {
		httpErr(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if errors.Is(err, errOverloaded) {
		w.Header().Set("Retry-After", "1")
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	httpErr(w, http.StatusServiceUnavailable, err.Error())
}

// Retry-After is a minimum pause, not a reservation. Concurrent callers and
// reconnects share the same scope bucket; retry with increasing jittered delay.
const overloadWriteTimeout = 100 * time.Millisecond
