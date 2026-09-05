package main

import (
	"context"
	"sync"
)

// sessionLock serializes browser access while allowing queued callers to cancel.
// Its zero value is ready for use, like sync.Mutex.
type sessionLock struct {
	once  sync.Once
	token chan struct{}
}

func (m *sessionLock) init() {
	m.once.Do(func() { m.token = make(chan struct{}, 1); m.token <- struct{}{} })
}
func (m *sessionLock) Lock()   { m.init(); <-m.token }
func (m *sessionLock) Unlock() { m.token <- struct{}{} }
func (m *sessionLock) LockContext(ctx context.Context) error {
	m.init()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
	}
	if err := ctx.Err(); err != nil {
		m.Unlock()
		return err
	}
	return nil
}
