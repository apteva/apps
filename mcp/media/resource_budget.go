package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sync"
)

// Weighted host admission bounds heavy encoders (2 units) while permitting
// short preview/index work (1 unit). A shared lease follows nested crop work.
type mediaLeaseKey struct{}
type mediaBudget struct {
	mu      sync.Mutex
	used    int
	changed chan struct{}
	waiters []*mediaWaiter
}

type mediaWaiter struct{ marker byte }

var mediaBudgets sync.Map

func acquireMediaWork(ctx context.Context, app *sdk.AppCtx, weight int) (context.Context, func(), error) {
	return acquireMediaWorkWithLeaseContext(ctx, ctx, app, weight)
}

func acquireMediaWorkWithLeaseContext(ctx, parent context.Context, app *sdk.AppCtx, weight int) (context.Context, func(), error) {
	if ctx.Err() != nil {
		return ctx, func() {}, ctx.Err()
	}
	if ctx.Value(mediaLeaseKey{}) != nil {
		return ctx, func() {}, nil
	}
	key := fmt.Sprint(remoteIndexerHostID(app))
	value, _ := mediaBudgets.LoadOrStore(key, &mediaBudget{changed: make(chan struct{})})
	budget := value.(*mediaBudget)
	capacity := parseConfigIntFallback(app.Config().Get("media_work_capacity"), 2)
	if weight > capacity {
		weight = capacity
	}
	waiter := &mediaWaiter{}
	budget.mu.Lock()
	budget.waiters = append(budget.waiters, waiter)
	budget.mu.Unlock()
	defer func() {
		budget.mu.Lock()
		for i, w := range budget.waiters {
			if w == waiter {
				budget.waiters = append(budget.waiters[:i], budget.waiters[i+1:]...)
				close(budget.changed)
				budget.changed = make(chan struct{})
				break
			}
		}
		budget.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return ctx, func() {}, ctx.Err()
		}
		budget.mu.Lock()
		if budget.waiters[0] == waiter && budget.used+weight <= capacity {
			budget.waiters = budget.waiters[1:]
			close(budget.changed)
			budget.changed = make(chan struct{})
			budget.used += weight
			budget.mu.Unlock()
			leaseCtx, cancel := context.WithCancel(parent)
			lease := &mediaLease{cancel: cancel}
			var once sync.Once
			return context.WithValue(leaseCtx, mediaLeaseKey{}, lease), func() {
				once.Do(func() {
					cancel()
					budget.mu.Lock()
					budget.used -= weight
					close(budget.changed)
					budget.changed = make(chan struct{})
					budget.mu.Unlock()
				})
			}, nil
		}
		changed := budget.changed
		budget.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx, func() {}, ctx.Err()
		case <-changed:
		}
	}
}
