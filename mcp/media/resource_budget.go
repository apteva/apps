package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"sync"
	"time"
)

// Weighted host admission bounds heavy encoders (2 units) while permitting
// short preview/index work (1 unit). A shared lease follows nested crop work.
type mediaLeaseKey struct{}
type mediaBudget struct {
	mu          sync.Mutex
	used        int
	changed     chan struct{}
	waiters     []*mediaWaiter
	leases      map[*mediaWaiter]time.Time
	nextLeaseID int64
}

type mediaWaiter struct {
	marker byte
	id     int64
	owner  string
	weight int
}
type mediaAdmissionObserverKey struct{}
type mediaAdmissionObserver struct {
	owner  string
	update func(map[string]any)
}

func withMediaAdmissionObserver(ctx context.Context, owner string, update func(map[string]any)) context.Context {
	return context.WithValue(ctx, mediaAdmissionObserverKey{}, mediaAdmissionObserver{owner, update})
}

// Caller holds budget.mu. Only job identities, never credentials, are exposed.
func (budget *mediaBudget) snapshot(waiter *mediaWaiter, capacity int) map[string]any {
	position := 0
	for i, w := range budget.waiters {
		if w == waiter {
			position = i + 1
			break
		}
	}
	leases := []map[string]any{}
	for w, start := range budget.leases {
		if len(leases) < 8 {
			leases = append(leases, map[string]any{"lease_id": w.id, "owner": w.owner, "work_units": w.weight, "age_ms": time.Since(start).Milliseconds()})
		}
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i]["lease_id"].(int64) < leases[j]["lease_id"].(int64) })
	return map[string]any{"admission_capacity": capacity, "admission_capacity_used": budget.used, "admission_queue_position": position, "admission_queue_length": len(budget.waiters), "admission_blocking_leases": leases}
}

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
	key := fmt.Sprint(componentExecutionID(app))
	value, _ := mediaBudgets.LoadOrStore(key, &mediaBudget{changed: make(chan struct{}), leases: map[*mediaWaiter]time.Time{}})
	budget := value.(*mediaBudget)
	capacity := parseConfigIntFallback(app.Config().Get("media_work_capacity"), 2)
	if weight > capacity {
		weight = capacity
	}
	observer, _ := ctx.Value(mediaAdmissionObserverKey{}).(mediaAdmissionObserver)
	owner := observer.owner
	if owner == "" {
		owner = "background_media_work"
	}
	waiter := &mediaWaiter{owner: owner, weight: weight}
	budget.mu.Lock()
	budget.nextLeaseID++
	waiter.id = budget.nextLeaseID
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
			budget.leases[waiter] = time.Now()
			snapshot := budget.snapshot(waiter, capacity)
			budget.mu.Unlock()
			if observer.update != nil {
				observer.update(snapshot)
			}
			leaseCtx, cancel := context.WithCancel(parent)
			lease := &mediaLease{cancel: cancel}
			var once sync.Once
			return context.WithValue(leaseCtx, mediaLeaseKey{}, lease), func() {
				once.Do(func() {
					cancel()
					budget.mu.Lock()
					budget.used -= weight
					delete(budget.leases, waiter)
					close(budget.changed)
					budget.changed = make(chan struct{})
					budget.mu.Unlock()
				})
			}, nil
		}
		changed := budget.changed
		snapshot := budget.snapshot(waiter, capacity)
		budget.mu.Unlock()
		if observer.update != nil {
			observer.update(snapshot)
		}
		select {
		case <-ctx.Done():
			return ctx, func() {}, ctx.Err()
		case <-changed:
		case <-time.After(10 * time.Second):
		}
	}
}
