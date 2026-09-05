package main

import (
	"context"
	"sync"
)

// Fixed-size striped gates bound bookkeeping and let cancelled cache waiters
// leave immediately; unrelated collisions only serialize, never share data.
type keyedGate struct {
	once  sync.Once
	slots [64]chan struct{}
}

func (g *keyedGate) acquire(ctx context.Context, key byte) (func(), error) {
	g.once.Do(func() {
		for i := range g.slots {
			g.slots[i] = make(chan struct{}, 1)
		}
	})
	slot := g.slots[int(key)%len(g.slots)]
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
