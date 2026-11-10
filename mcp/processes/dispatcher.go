package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const dispatchRepairInterval = time.Minute

// SQLite holds the work. The signal only tells the dispatcher which project's
// deadlines may have changed since its last pass.
type processDispatcher struct {
	app     *App
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	mu      sync.Mutex
	pending map[string]struct{}
}

type projectDispatch struct {
	next, syncRetry, triggerRetry time.Time
}

func newProcessDispatcher(app *App) *processDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &processDispatcher{app: app, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (a *App) wakeDispatch(project string) {
	d := a.dispatcher
	if d == nil || project == "" || d.ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	if d.pending == nil {
		d.pending = make(map[string]struct{})
	}
	d.pending[project] = struct{}{}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *processDispatcher) takeWakes() map[string]struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	pending := d.pending
	d.pending = nil
	return pending
}

func (d *processDispatcher) visibleProjects() ([]string, error) {
	if fixed := d.app.ctx.CurrentProject(); fixed != "" {
		return []string{fixed}, nil
	}
	api := d.app.ctx.PlatformAPI()
	if api == nil {
		return nil, errors.New("platform project discovery unavailable")
	}
	projects, err := api.ListProjects()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.ID != "" {
			ids = append(ids, p.ID)
		}
	}
	return ids, nil
}

func (d *processDispatcher) run() {
	defer close(d.done)
	states := map[string]projectDispatch{}
	var repairAt time.Time // Recover committed work on startup.
	for d.ctx.Err() == nil {
		pending := d.takeWakes()
		now := time.Now()
		repair := !now.Before(repairAt)
		for project, state := range states {
			if !state.next.IsZero() && !now.Before(state.next) {
				if pending == nil {
					pending = make(map[string]struct{})
				}
				pending[project] = struct{}{}
			}
		}
		if repair || len(pending) > 0 {
			visible, err := d.visibleProjects()
			if err != nil {
				d.app.ctx.Logger().Warn("Processes dispatcher could not list visible projects", "err", err)
				// Rebuild from SQLite once the platform can authorize work.
				clear(states)
				repairAt = time.Now().Add(5 * time.Second)
			} else {
				current := make(map[string]projectDispatch, len(visible))
				for _, project := range visible {
					if d.ctx.Err() != nil {
						return
					}
					state, known := states[project]
					_, requested := pending[project]
					if repair || requested || !known {
						state = d.dispatchProject(project, state)
					}
					current[project] = state
				}
				states = current
				if repair {
					repairAt = time.Now().Add(dispatchRepairInterval)
				}
			}
		}

		next := repairAt
		for _, state := range states {
			next = earlierDeadline(next, state.next)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-d.ctx.Done():
		case <-d.app.ctx.Done():
			timer.Stop()
			return
		case <-d.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (d *processDispatcher) dispatchProject(project string, state projectDispatch) projectDispatch {
	a, ctx := d.app, d.ctx
	now := time.Now()
	var syncErr, triggerErr error
	if !now.Before(state.syncRetry) {
		state.syncRetry = time.Time{}
		syncErr = a.retryPending(ctx, project)
		if syncErr != nil {
			state.syncRetry = time.Now().Add(30 * time.Second)
		}
	}
	if !now.Before(state.triggerRetry) {
		state.triggerRetry = time.Time{}
		triggerErr = a.tickTriggers(ctx, project)
		if triggerErr != nil {
			state.triggerRetry = time.Now().Add(5 * time.Second)
		}
	}

	checkedAt := time.Now().UTC()
	runErr := a.tickDirect(ctx, checkedAt, project)
	publishErr := a.drainEvents(ctx, project)
	cleanupErr := a.cleanupWorkers(ctx, time.Now().UTC(), project)
	queueAt, queueErr := a.nextQueueWake(ctx, project)
	runAt, deadlineErr := a.nextRunWake(ctx, project, checkedAt)
	now = time.Now()
	if (cleanupErr != nil || publishErr != nil) && !queueAt.IsZero() && !queueAt.After(now) {
		// A failure before saving backoff must not spin on an overdue row.
		queueAt = now.Add(time.Second)
	}
	state.next = earlierDeadline(earlierDeadline(queueAt, runAt), earlierDeadline(state.syncRetry, state.triggerRetry))
	if err := errors.Join(runErr, queueErr, deadlineErr); err != nil {
		state.next = earlierDeadline(state.next, now.Add(5*time.Second))
	}
	if err := errors.Join(syncErr, triggerErr, runErr, cleanupErr, publishErr, queueErr, deadlineErr); err != nil && ctx.Err() == nil {
		a.ctx.Logger().Warn("Processes dispatch incomplete", "project", project, "err", err)
	}
	return state
}

func earlierDeadline(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// UTC RFC3339Nano omits trailing fractional zeros. Removing the trailing Z
// before comparing makes whole seconds and fractional seconds sort correctly.
func deadlineKey(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.999999999")
}

func parseDeadlineKey(key string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, key+"Z")
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid Processes deadline: %w", err)
	}
	return t, nil
}
