package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"sync"
	"time"
)

const routingDispatchProjects = 256
const routingDispatchWorkers = 4

type routingWake struct {
	due            time.Time
	running, dirty bool
}

// A wake is a hint after commit. The database remains authoritative. Queue
// overflow deliberately falls back to the existing periodic recovery workers.
type routingDispatcher struct {
	mu     sync.Mutex
	tasks  map[string]*routingWake
	ready  chan struct{}
	jobs   chan string
	stop   chan struct{}
	wg     sync.WaitGroup
	closed bool
	run    func(string) time.Time
}

func newRoutingDispatcher(run func(string) time.Time) *routingDispatcher {
	d := &routingDispatcher{tasks: map[string]*routingWake{}, ready: make(chan struct{}, 1), jobs: make(chan string, routingDispatchWorkers), stop: make(chan struct{}), run: run}
	d.wg.Add(1)
	go d.schedule()
	for range routingDispatchWorkers {
		d.wg.Add(1)
		go d.work()
	}
	return d
}
func (d *routingDispatcher) signal() {
	select {
	case d.ready <- struct{}{}:
	default:
	}
}
func (d *routingDispatcher) wake(project string) bool {
	if project == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	w := d.tasks[project]
	if w == nil {
		if len(d.tasks) >= routingDispatchProjects {
			return false
		}
		w = &routingWake{}
		d.tasks[project] = w
	}
	w.dirty = true
	w.due = time.Now()
	d.signal()
	return true
}
func (d *routingDispatcher) schedule() {
	defer d.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		d.mu.Lock()
		now := time.Now()
		next := now.Add(time.Hour)
		for project, w := range d.tasks {
			if w.running {
				continue
			}
			if !w.due.After(now) {
				select {
				case d.jobs <- project:
					w.running = true
					w.dirty = false
				default:
				}
				continue
			}
			if w.due.Before(next) {
				next = w.due
			}
		}
		d.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(max(time.Until(next), time.Millisecond))
		select {
		case <-d.stop:
			return
		case <-d.ready:
		case <-timer.C:
		}
	}
}
func (d *routingDispatcher) work() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stop:
			return
		case project := <-d.jobs:
			select {
			case <-d.stop:
				return
			default:
			}
			due := d.run(project)
			d.mu.Lock()
			w := d.tasks[project]
			w.running = false
			if w.dirty {
				w.due = time.Now()
			} else if due.IsZero() {
				delete(d.tasks, project)
			} else {
				w.due = due
			}
			d.mu.Unlock()
			d.signal()
		}
	}
}
func (d *routingDispatcher) close() {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.stop)
	}
	d.mu.Unlock()
	d.wg.Wait()
}

func (a *App) startRoutingDispatcher(ctx *sdk.AppCtx) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	workCtx, cancel := context.WithCancel(context.Background())
	a.dispatchCancel = cancel
	a.eventDispatcher = newRoutingDispatcher(func(project string) time.Time {
		done, admissionErr := a.runtimeBegin(false)
		if admissionErr != nil {
			return time.Time{}
		}
		defer done()
		if err := a.publishLifecycleEventsContext(workCtx, ctx.WithProject(project), ""); err != nil {
			ctx.Logger().Warn("routing event delivery", "project", project, "err", err)
		}
		if err := a.publishAccountEvents(workCtx, ctx.WithProject(project)); err != nil {
			ctx.Logger().Warn("carrier account event delivery", "project", project, "err", err)
		}
		return time.Time{}
	})
	deadlines := routingDeadlineTracker{}
	a.dispatcher = newRoutingDispatcher(func(project string) time.Time {
		done, admissionErr := a.runtimeBegin(false)
		if admissionErr != nil {
			return time.Time{}
		}
		defer done()
		scoped := ctx.WithProject(project)
		if workCtx.Err() != nil {
			return time.Time{}
		}
		var before int64
		_ = scoped.AppDB().QueryRow(`SELECT revision FROM telephony_read_versions WHERE project_id=?`, project).Scan(&before)
		// These use the same atomic claims as periodic recovery, including when a
		// periodic worker runs concurrently with an immediate wake.
		if err := a.runDecisionTick(workCtx, scoped); err != nil {
			ctx.Logger().Warn("routing dispatch", "project", project, "err", err)
		}
		if err := a.runRingGroupTick(workCtx, scoped); err != nil {
			ctx.Logger().Warn("ring dispatch", "project", project, "err", err)
		}
		if err := a.runRoutingEffects(workCtx, scoped); err != nil {
			ctx.Logger().Warn("routing effects", "project", project, "err", err)
		}
		if err := a.flushDecisionMarks(project); err != nil {
			ctx.Logger().Warn("routing outcomes", "project", project, "err", err)
		}
		if err := a.runRingLegTick(workCtx, scoped); err != nil {
			ctx.Logger().Warn("ring leg dispatch", "project", project, "err", err)
		}
		if err := a.runAutoAnswerTick(workCtx, scoped); err != nil {
			ctx.Logger().Warn("answer dispatch", "project", project, "err", err)
		}
		var after int64
		if err := scoped.AppDB().QueryRow(`SELECT revision FROM telephony_read_versions WHERE project_id=?`, project).Scan(&after); err == nil && after != before {
			a.callChanges.notify(project)
		}
		a.dispatchMu.Lock()
		if a.eventDispatcher != nil {
			a.eventDispatcher.wake(project)
		}
		a.dispatchMu.Unlock()

		var raw string
		err := ctx.AppDB().QueryRow(`SELECT COALESCE(MIN(deadline),'') FROM (
   SELECT CASE WHEN status='pending' AND not_before<>'' THEN MIN(not_before,deadline_at) ELSE deadline_at END AS deadline FROM routing_decisions WHERE project_id=? AND status IN ('pending','running')
   UNION ALL SELECT next_attempt_at FROM routing_effects WHERE project_id=? AND status='pending'
   UNION ALL SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 seconds') FROM call_ring_runs WHERE project_id=? AND status IN ('ringing','exhausted')
   UNION ALL SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 seconds') FROM call_legs INDEXED BY idx_call_legs_active WHERE project_id=? AND status NOT IN ('finished','canceled')
   UNION ALL SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 seconds') FROM calls INDEXED BY idx_calls_pending_answer WHERE project_id=? AND direction='inbound' AND status='pending' AND handling_reason=''
   UNION ALL SELECT o.expires_at FROM call_offers o JOIN calls c ON c.id=o.call_id WHERE o.project_id=? AND o.status='offered' AND c.status='pending'
  )`, project, project, project, project, project, project).Scan(&raw)
		if err != nil {
			return time.Now().Add(time.Second)
		}
		return deadlines.next(project, raw, after, time.Now())
	})
}
func (a *App) stopRoutingDispatcher() {
	a.dispatchMu.Lock()
	a.decisionStopping = true
	if a.dispatchCancel != nil {
		a.dispatchCancel()
		a.dispatchCancel = nil
	}
	d := a.dispatcher
	events := a.eventDispatcher
	a.dispatcher = nil
	a.eventDispatcher = nil
	a.dispatchMu.Unlock()
	if d != nil {
		d.close()
	}
	if events != nil {
		events.close()
	}
	a.decisionWG.Wait()
}
func (a *App) wakeRouting(project string) {
	a.dispatchMu.Lock()
	d := a.dispatcher
	if d != nil {
		d.wake(project)
	}
	a.dispatchMu.Unlock()
}
func (a *App) routingCommitted(project string) { a.callChanges.notify(project); a.wakeRouting(project) }
func (c *callsDB) committed(project string) {
	if c.afterCommit != nil {
		c.afterCommit(project)
	}
}
func (c *callsDB) commitCall(tx interface{ Commit() error }, callID string) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	if c.afterCommit != nil {
		var project string
		if err := c.db.QueryRow(`SELECT project_id FROM calls WHERE id=?`, callID).Scan(&project); err == nil {
			c.committed(project)
		}
	}
	return nil
}

func (a *App) routingCapacityReleased() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	d := a.dispatcher
	if d == nil {
		return
	}
	d.mu.Lock()
	for _, w := range d.tasks {
		if !w.running {
			w.due = time.Now()
			w.dirty = true
		}
	}
	d.mu.Unlock()
	d.signal()
}

// Startup/overflow reconciliation is deliberately slower than live timers.
// Commits and active ring deadlines drive the primary dispatcher.
func (a *App) runRoutingReconciliation(c context.Context, ctx *sdk.AppCtx) error {
	a.dispatchMu.Lock()
	d := a.dispatcher
	queued := d != nil && d.wake(ctx.CurrentProject())
	a.dispatchMu.Unlock()
	if queued {
		return nil
	}
	if err := a.runDecisionTick(c, ctx); err != nil {
		return err
	}
	if err := a.runRingGroupTick(c, ctx); err != nil {
		return err
	}
	return a.runRingLegTick(c, ctx)
}

// Retained expired/invalid deadlines are retryable evidence, not a 10Hz loop.
// A fresh commit still overrides this backoff immediately through wake().
func boundedRoutingDeadline(raw string, now time.Time) time.Time {
	due, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !due.After(now) {
		return now.Add(time.Second)
	}
	return due
}

// The first overdue observation can be ordinary work that expired while this
// pass was running. Retry it immediately. Back off only a repeated unchanged
// overdue record; commit wakes still preempt the scheduled retry.
type routingDeadlineStamp struct {
	raw      string
	revision int64
}
type routingDeadlineTracker struct {
	mu   sync.Mutex
	last map[string]routingDeadlineStamp
}

func (d *routingDeadlineTracker) next(project, raw string, revision int64, now time.Time) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	due, err := time.Parse(time.RFC3339Nano, raw)
	if raw == "" || (err == nil && due.After(now)) {
		delete(d.last, project)
		if raw == "" {
			return time.Time{}
		}
		return due
	}
	stamp := routingDeadlineStamp{raw, revision}
	previous, exists := d.last[project]
	if d.last == nil {
		d.last = make(map[string]routingDeadlineStamp)
	}
	if !exists && len(d.last) >= routingDispatchProjects {
		for key := range d.last {
			delete(d.last, key)
			break
		}
	}
	d.last[project] = stamp
	if exists && previous == stamp {
		return boundedRoutingDeadline(raw, now)
	}
	return now.Add(time.Millisecond)
}
