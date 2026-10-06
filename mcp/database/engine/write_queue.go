package engine

import (
	"context"
	"sync"
)

// A backend may combine independently atomic record transactions into a single
// durable commit. Schema changes and reads still use the database lock directly.
type transactionCall struct {
	ctx context.Context
	run func(transaction) error
}
type groupedBackend interface {
	transactions([]transactionCall) []error
}
type queuedWrite struct {
	call transactionCall
	out  any
	err  error
	done chan struct{}
}
type writeQueue struct {
	mu      sync.Mutex
	pending []*queuedWrite
	running bool
}

const maxQueuedWrites = 64

func (d *database) enqueueWrite(ctx context.Context, b groupedBackend, run func(transaction) (any, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &queuedWrite{done: make(chan struct{})}
	w.call = transactionCall{ctx: ctx, run: func(tx transaction) (err error) { w.out, err = run(tx); return err }}
	d.writes.mu.Lock()
	if len(d.writes.pending) >= maxQueuedWrites {
		d.writes.mu.Unlock()
		return nil, fail("resource_limit", "database write queue is full")
	}
	d.writes.pending = append(d.writes.pending, w)
	if !d.writes.running {
		d.writes.running = true
		go d.drainWrites(b)
	}
	d.writes.mu.Unlock()
	// Once dispatched, wait for the definite commit outcome. The backend checks
	// cancellation before staging a request. Cancellation after staging cannot
	// undo an accepted write or let us acknowledge it before the durable commit.
	<-w.done
	if w.err != nil {
		return nil, w.err
	}
	return w.out, nil
}

func (d *database) drainWrites(b groupedBackend) {
	for {
		d.writes.mu.Lock()
		group := d.writes.pending
		d.writes.pending = nil
		if len(group) == 0 {
			d.writes.running = false
			d.writes.mu.Unlock()
			return
		}
		d.writes.mu.Unlock()
		calls := make([]transactionCall, len(group))
		for i, w := range group {
			calls[i] = w.call
		}
		d.mu.Lock()
		if d.closed {
			for _, w := range group {
				w.err = fail("not_found", "database is closed")
			}
		} else {
			errs := b.transactions(calls)
			for i, w := range group {
				w.err = errs[i]
			}
		}
		d.mu.Unlock()
		for _, w := range group {
			close(w.done)
		}
	}
}
