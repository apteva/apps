package main

import (
	"context"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errProjectionQueueDeadline = errors.New("projection worker queue deadline")

type capacityKind uint8

const (
	interactiveCapacityKind capacityKind = iota
	projectionCapacityKind
)

type projectionTimingKey struct{}
type projectionCapacityHeldKey struct{}
type interactiveCapacityHeldKey struct{}
type projectionTimingState struct {
	sqlExecution                                            time.Duration
	workerQueue, readQueue, writeLock, staging, publication time.Duration
}

// capacityState admits interactive reads and projection calculations through
// separate class limits while enforcing one shared ceiling. Limits are
// changed in place so configuration reloads cannot orphan permits held by
// in-flight calls.
type capacityState struct {
	readLimit, workerLimit, totalLimit int
	activeRead, activeWorker           int
	wake                               chan struct{}
}

func (a *App) ensureCapacityLimits(ctx *sdk.AppCtx) {
	read := maxReadConns(ctx)
	workers := maxProjectionWorkers(ctx)
	total := maxTotalConcurrency(ctx)
	// Both classes need one slot. A total of one would permanently starve one
	// class, so normalize it to the smallest useful cap.
	if total < 2 {
		total = 2
	}
	if read > total-1 {
		read = total - 1
	}
	if workers > total-1 {
		workers = total - 1
	}
	a.capacityMu.Lock()
	defer a.capacityMu.Unlock()
	if a.capacity == nil {
		a.capacity = &capacityState{wake: make(chan struct{})}
	}
	a.capacity.readLimit = read
	a.capacity.workerLimit = workers
	a.capacity.totalLimit = total
}

func (a *App) acquireCapacity(ctx context.Context, app *sdk.AppCtx, kind capacityKind) (func(), time.Duration, error) {
	a.ensureCapacityLimits(app)
	start := time.Now()
	queueMS := maxReadQueueMs(app)
	deadlineErr := error(errReadQueueDeadline)
	if kind == projectionCapacityKind {
		queueMS = maxProjectionQueueMs(app)
		deadlineErr = errProjectionQueueDeadline
	}
	queueCtx, cancel := context.WithTimeoutCause(ctx, time.Duration(queueMS)*time.Millisecond, deadlineErr)
	defer cancel()
	for {
		a.capacityMu.Lock()
		st := a.capacity
		allowed := st.activeRead+st.activeWorker < st.totalLimit
		if kind == interactiveCapacityKind {
			allowed = allowed && st.activeRead < st.readLimit
		} else {
			allowed = allowed && st.activeWorker < st.workerLimit
		}
		if allowed {
			if kind == interactiveCapacityKind {
				st.activeRead++
			} else {
				st.activeWorker++
			}
			a.capacityMu.Unlock()
			return func() {
				a.capacityMu.Lock()
				if kind == interactiveCapacityKind {
					st.activeRead--
				} else {
					st.activeWorker--
				}
				old := st.wake
				st.wake = make(chan struct{})
				close(old)
				a.capacityMu.Unlock()
			}, time.Since(start), nil
		}
		wake := st.wake
		a.capacityMu.Unlock()
		select {
		case <-wake:
		case <-queueCtx.Done():
			return nil, time.Since(start), queueCtx.Err()
		}
	}
}

func operationNeedsInteractiveCapacity(operation string) bool {
	switch operation {
	case "tables_list", "tables_describe", "tables_query",
		"rows_get", "rows_search", "rows_count", "rows_aggregate",
		"indexes_list", "projections_list", "projections_describe", "projections_status", "diagnostics_list":
		return true
	default:
		return false
	}
}
