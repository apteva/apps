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
	a.eventDispatcher = newRoutingDispatcher(func(project string) time.Time {
		if err := a.publishLifecycleEvents(ctx.WithProject(project), ""); err != nil {
			ctx.Logger().Warn("routing event delivery", "project", project, "err", err)
		}
		return time.Time{}
	})
	a.dispatcher = newRoutingDispatcher(func(project string) time.Time {
		scoped := ctx.WithProject(project)
		// These use the same atomic claims as periodic recovery, including when a
		// periodic worker runs concurrently with an immediate wake.
		if err := a.runDecisionTick(context.Background(), scoped); err != nil {
			ctx.Logger().Warn("routing dispatch", "project", project, "err", err)
		}
		if err := a.runRingGroupTick(context.Background(), scoped); err != nil {
			ctx.Logger().Warn("ring dispatch", "project", project, "err", err)
		}
		if err := a.flushDecisionMarks(project); err != nil {
			ctx.Logger().Warn("routing outcomes", "project", project, "err", err)
		}
		a.callChanges.notify(project)
		a.dispatchMu.Lock()
		if a.eventDispatcher != nil {
			a.eventDispatcher.wake(project)
		}
		a.dispatchMu.Unlock()

		var raw string
		err := ctx.AppDB().QueryRow(`SELECT COALESCE(MIN(deadline),'') FROM (
   SELECT deadline_at AS deadline FROM routing_decisions WHERE project_id=? AND status IN ('pending','running')
   UNION ALL SELECT o.expires_at FROM call_offers o JOIN calls c ON c.id=o.call_id WHERE o.project_id=? AND o.status='offered' AND c.status='pending'
  )`, project, project).Scan(&raw)
		if err != nil || raw == "" {
			return time.Time{}
		}
		due, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return time.Time{}
		}
		// An already-expired record retained by a transient error must not spin.
		if !due.After(time.Now()) {
			return time.Now().Add(100 * time.Millisecond)
		}
		return due
	})
}
func (a *App) stopRoutingDispatcher() {
	a.dispatchMu.Lock()
	a.decisionStopping = true
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
