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
}

var mediaBudgets sync.Map

func acquireMediaWork(ctx context.Context, app *sdk.AppCtx, weight int) (context.Context, func(), error) {
	if ctx.Value(mediaLeaseKey{}) != nil {
		return ctx, func() {}, nil
	}
	key := fmt.Sprint(remoteIndexerHostID(app))
	value, _ := mediaBudgets.LoadOrStore(key, &mediaBudget{changed: make(chan struct{})})
	budget := value.(*mediaBudget)
	capacity := parseConfigIntFallback(app.Config().Get("media_work_capacity"), 4)
	if weight > capacity {
		weight = capacity
	}
	for {
		budget.mu.Lock()
		if budget.used+weight <= capacity {
			budget.used += weight
			budget.mu.Unlock()
			return context.WithValue(ctx, mediaLeaseKey{}, true), func() {
				budget.mu.Lock()
				budget.used -= weight
				close(budget.changed)
				budget.changed = make(chan struct{})
				budget.mu.Unlock()
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
