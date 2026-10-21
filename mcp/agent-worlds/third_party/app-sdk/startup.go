package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// startupStatus gates every app route until OnMount completes. Publishing the
// handler under the mutex also publishes the completed route/auth registry.
type startupStatus struct {
	mu               sync.RWMutex
	state            string
	phase            string
	completed, total int64
	started          time.Time
	handler          http.Handler
}

func newStartupStatus() *startupStatus {
	return &startupStatus{state: "initializing", phase: "starting", started: time.Now()}
}
func (s *startupStatus) progress(phase string, completed, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "initializing" {
		return
	}
	if len(phase) > 128 {
		phase = phase[:128]
	}
	s.phase, s.completed, s.total = phase, completed, total
}
func (s *startupStatus) failIfInitializing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "initializing" {
		s.state = "failed"
	}
}
func (s *startupStatus) ready(ctx context.Context, handler http.Handler) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "initializing" || ctx.Err() != nil {
		return false
	}
	s.state, s.handler = "ready", handler
	return true
}
func (s *startupStatus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	handler := s.handler
	body := map[string]any{"ok": false, "status": s.state, "phase": s.phase, "completed": s.completed, "total": s.total, "elapsed_ms": time.Since(s.started).Milliseconds()}
	s.mu.RUnlock()
	if handler != nil {
		handler.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.URL.Path != "/health" {
		body = map[string]any{"error": "app is not ready"}
	}
	_ = json.NewEncoder(w).Encode(body)
}
