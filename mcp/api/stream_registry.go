package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type streamRegistration struct {
	project               string
	apiID, routeID, keyID int64
	cancel                context.CancelFunc
}
type streamRegistry struct {
	mu     sync.Mutex
	next   uint64
	active map[uint64]streamRegistration
}

func (s *streamRegistry) register(parent context.Context, project string, apiID, routeID, keyID int64, expiry time.Time) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) >= 128 {
		return nil, nil, errors.New("stream connection limit reached")
	}
	if s.active == nil {
		s.active = make(map[uint64]streamRegistration)
	}
	ctx, cancel := context.WithCancel(parent)
	if !expiry.IsZero() {
		cancel()
		ctx, cancel = context.WithDeadline(parent, expiry)
	}
	s.next++
	id := s.next
	s.active[id] = streamRegistration{project, apiID, routeID, keyID, cancel}
	return ctx, func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock(); cancel() }, nil
}
func (s *streamRegistry) cancelMatching(project string, apiID, routeID, keyID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.active {
		if r.project == project && (apiID == 0 || r.apiID == apiID) && (routeID == 0 || r.routeID == routeID) && (keyID == 0 || r.keyID == keyID) {
			r.cancel()
		}
	}
}
func (s *streamRegistry) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.active {
		r.cancel()
	}
}
