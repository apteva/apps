package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errPasswordBusy = errors.New("password verification capacity exhausted")
var errPasswordWaitExpired = errors.New("password verification queue wait expired")

// Four Argon2 computations retain the existing memory ceiling. A small bounded
// queue absorbs login bursts without spawning more hashing work or retaining
// unbounded passwords. Request cancellation removes a waiting attempt promptly.
type passwordGate struct {
	active, admitted chan struct{}
	wait             time.Duration
}

func newPasswordGate(concurrency, waiters int, wait time.Duration) *passwordGate {
	return &passwordGate{make(chan struct{}, concurrency), make(chan struct{}, concurrency+waiters), wait}
}

var passwordWork = newPasswordGate(4, 64, 10*time.Second)

func (g *passwordGate) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.admitted <- struct{}{}:
	default:
		return errPasswordBusy
	}
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.active <- struct{}{}:
		if err := ctx.Err(); err != nil {
			g.release()
			return err
		}
		return nil
	case <-ctx.Done():
		<-g.admitted
		return ctx.Err()
	case <-timer.C:
		<-g.admitted
		return errPasswordWaitExpired
	}
}
func (g *passwordGate) release()                    { <-g.active; <-g.admitted }
func acquirePasswordHash(ctx context.Context) error { return passwordWork.acquire(ctx) }
func releasePasswordHash()                          { passwordWork.release() }
func passwordContext(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	// Internal/MCP callers without an HTTP context still have the bounded queue
	// deadline; HTTP entry points pass their originating request context.
	return context.Background()
}
func passwordFailureStatus(err error) int {
	if errors.Is(err, errPasswordBusy) || errors.Is(err, errPasswordWaitExpired) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusRequestTimeout
	}
	return http.StatusInternalServerError
}
func writePasswordFailure(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	httpErr(w, passwordFailureStatus(err), "password verification unavailable")
}
