package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Long-lived connections (app_events SSE streams and WebSocket connections)
// register here so credential, route and API changes can end them at once.
// The cancel cause tells the connection why it ended.
var (
	errStreamCredentialRevoked = errors.New("credential revoked")
	errStreamCredentialExpired = errors.New("credential expired")
	errStreamConfigChanged     = errors.New("route configuration changed")
	errStreamShutdown          = errors.New("gateway shutting down")
)

const (
	streamKindSSE       = "sse"
	streamKindWebSocket = "websocket"
	maxSSEStreams       = 128
	maxWebSocketConns   = 512
)

type streamLimitError struct{ message string }

func (e *streamLimitError) Error() string { return e.message }

type streamRegistration struct {
	kind                  string
	project               string
	apiID, routeID, keyID int64
	cancel                context.CancelCauseFunc
}
type streamRegistry struct {
	mu     sync.Mutex
	next   uint64
	active map[uint64]streamRegistration
}

func (s *streamRegistry) register(parent context.Context, project string, apiID, routeID, keyID int64, expiry time.Time) (context.Context, func(), error) {
	return s.registerKind(parent, streamKindSSE, project, apiID, routeID, keyID, expiry, 0)
}

// registerKind admits one connection. perKeyRoute > 0 caps open connections
// of this kind for the same key on the same route.
func (s *streamRegistry) registerKind(parent context.Context, kind, project string, apiID, routeID, keyID int64, expiry time.Time, perKeyRoute int) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = make(map[uint64]streamRegistration)
	}
	total, sameKey := 0, 0
	for _, r := range s.active {
		if r.kind != kind {
			continue
		}
		total++
		if keyID != 0 && r.keyID == keyID && r.routeID == routeID && r.project == project {
			sameKey++
		}
	}
	switch {
	case kind == streamKindSSE && total >= maxSSEStreams:
		return nil, nil, &streamLimitError{"stream connection limit reached"}
	case kind == streamKindWebSocket && total >= maxWebSocketConns:
		return nil, nil, &streamLimitError{"websocket connection limit reached"}
	case perKeyRoute > 0 && keyID != 0 && sameKey >= perKeyRoute:
		return nil, nil, &streamLimitError{"too many open connections for this credential"}
	}
	ctx, cancel := context.WithCancelCause(parent)
	if !expiry.IsZero() {
		var stopDeadline context.CancelFunc
		ctx, stopDeadline = context.WithDeadlineCause(ctx, expiry, errStreamCredentialExpired)
		outer := cancel
		cancel = func(cause error) { outer(cause); stopDeadline() }
	}
	s.next++
	id := s.next
	s.active[id] = streamRegistration{kind, project, apiID, routeID, keyID, cancel}
	return ctx, func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock(); cancel(context.Canceled) }, nil
}

func (s *streamRegistry) cancelMatching(project string, apiID, routeID, keyID int64) {
	cause := errStreamConfigChanged
	if keyID != 0 {
		cause = errStreamCredentialRevoked
	}
	s.cancelMatchingCause(project, apiID, routeID, keyID, cause)
}

func (s *streamRegistry) cancelMatchingCause(project string, apiID, routeID, keyID int64, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.active {
		if r.project == project && (apiID == 0 || r.apiID == apiID) && (routeID == 0 || r.routeID == routeID) && (keyID == 0 || r.keyID == keyID) {
			r.cancel(cause)
		}
	}
}
func (s *streamRegistry) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.active {
		r.cancel(errStreamShutdown)
	}
}

func (s *streamRegistry) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.active {
		if r.kind == kind {
			n++
		}
	}
	return n
}
