package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

// opRegistry tracks everything that must end when a session is stopped:
// the in-flight action's context and any live streams. Stop reaches these
// without taking the phone's lock, so it never queues behind a busy action.
type opRegistry struct {
	mu       sync.Mutex
	next     int
	ops      map[string]map[int]func()
	stopping map[string]bool
}

func newOpRegistry() *opRegistry {
	return &opRegistry{ops: map[string]map[int]func(){}, stopping: map[string]bool{}}
}

// track registers a cancel function for session sid and returns its remover.
func (r *opRegistry) track(sid string, cancel func()) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	n := r.next
	if r.ops[sid] == nil {
		r.ops[sid] = map[int]func(){}
	}
	r.ops[sid][n] = cancel
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.ops[sid], n)
		if len(r.ops[sid]) == 0 {
			delete(r.ops, sid)
		}
	}
}

// halt marks sid as stopping and cancels everything registered for it.
func (r *opRegistry) halt(sid string) {
	r.mu.Lock()
	r.stopping[sid] = true
	cancels := []func(){}
	for _, c := range r.ops[sid] {
		cancels = append(cancels, c)
	}
	r.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// cancelAll ends a session's registered work without marking it stopping;
// used when a session ends normally (close, expiry, device lost).
func (r *opRegistry) cancelAll(sid string) {
	r.mu.Lock()
	cancels := []func(){}
	for _, c := range r.ops[sid] {
		cancels = append(cancels, c)
	}
	r.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

func (r *opRegistry) isStopping(sid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping[sid]
}

func (r *opRegistry) done(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stopping, sid)
}

// endReason maps a terminal session status to the reason reported in events.
func endReason(status string) string {
	switch status {
	case "closed", "stopped", "expired", "interrupted", "device_lost":
		return status
	}
	return ""
}

// recordEnd stores when and why a session ended and announces it.
func (e *engine) recordEnd(s Session, status, stopReason, stoppedBy string) {
	reason := endReason(status)
	if reason == "" {
		return
	}
	now := time.Now()
	_, _ = e.db.Exec(`INSERT INTO mobile_session_meta(session_id,ended_at,end_reason,stop_reason,stopped_by) VALUES (?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET ended_at=excluded.ended_at,end_reason=excluded.end_reason,stop_reason=excluded.stop_reason,stopped_by=excluded.stopped_by`,
		s.ID, now.Unix(), reason, stopReason, stoppedBy)
	started := time.Unix(s.CreatedAt, 0)
	data := sessionEventData(s)
	data["ended_at"] = now.UTC().Format(time.RFC3339)
	data["duration_ms"] = now.Sub(started).Milliseconds()
	data["end_reason"] = reason
	if stopReason != "" {
		data["stop_reason"] = stopReason
	}
	e.events.add(s.ProjectID, "mobile.session.ended", data)
}

func sessionEventData(s Session) map[string]any {
	return map[string]any{"session_id": s.ID, "device_id": s.DeviceID, "caller_ref": s.CallerRef, "started_at": time.Unix(s.CreatedAt, 0).UTC().Format(time.RFC3339)}
}

// stop ends one, a device's, or all of a project's sessions immediately.
func (e *engine) stop(ctx context.Context, pid string, args map[string]any) (any, error) {
	reason := strings.TrimSpace(str(args, "reason"))
	by := strings.TrimSpace(str(args, "stopped_by"))
	if len(reason) > 500 || len(by) > 200 {
		return nil, problem("invalid_argument", "reason or stopped_by too long")
	}
	if by == "" {
		by = "mobile_session"
	}
	var targets []Session
	switch {
	case str(args, "session_id") != "":
		s, err := e.session(pid, str(args, "session_id"))
		if err != nil {
			return nil, err
		}
		targets = []Session{s}
	case str(args, "device_id") != "":
		s, err := e.sessionForDevice(str(args, "device_id"))
		if err == nil && s.ProjectID == pid {
			targets = []Session{s}
		}
	case boolean(args, "all"):
		all, err := e.allActive()
		if err != nil {
			return nil, err
		}
		for _, s := range all {
			if s.ProjectID == pid {
				targets = append(targets, s)
			}
		}
	default:
		return nil, problem("invalid_argument", "stop needs session_id, device_id or all=true")
	}
	stopped := []Session{}
	var firstErr error
	for _, s := range targets {
		fresh, err := e.stopOne(ctx, s, reason, by)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err == nil {
			stopped = append(stopped, fresh)
		}
	}
	if firstErr != nil && len(stopped) == 0 {
		return nil, firstErr
	}
	return map[string]any{"stopped": stopped, "count": len(stopped)}, nil
}

func (e *engine) stopOne(ctx context.Context, s Session, reason, by string) (Session, error) {
	if s.Status != "active" && s.Status != "opening" && s.Status != "paused" && s.Status != "cleanup_failed" {
		return s, nil // already ended: stopping is idempotent
	}
	e.ops.halt(s.ID)
	defer e.ops.done(s.ID)
	e.cancelRunsFor(s.ID)
	// Lift any finger the cancelled command left on the screen. This runs
	// beside the lease release; it never blocks a stop on a slow phone.
	release := make(chan struct{})
	go func() {
		defer close(release)
		if s.Device.Driver == "adb" {
			rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			e.drivers.releaseTouch(rctx, s.Device.UDID)
		}
	}()
	g := e.gate(s.DeviceID)
	g.mu.Lock()
	fresh, err := e.session(s.ProjectID, s.ID)
	if err == nil {
		err = e.finishWait(ctx, fresh, g, "stopped", 5*time.Second, reason, by)
	}
	g.mu.Unlock()
	select {
	case <-release:
	case <-time.After(400 * time.Millisecond):
		// Reported done; the release finishes in the background.
	}
	if err != nil {
		return s, err
	}
	return e.session(s.ProjectID, s.ID)
}

// cancelRunsFor cancels a test run driving session sid, if any.
func (e *engine) cancelRunsFor(sid string) {
	rows, err := e.db.Query(`SELECT id FROM mobile_runs WHERE session_id=? AND status IN ('queued','running')`, sid)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		if cancel := e.runs[id]; cancel != nil {
			cancel()
		}
	}
}

// releaseTouch ends any gesture a killed command left half done: it stops
// the platform input tool and Mobile's own input helper, then cancels the
// injected pointer stream.
func (d *drivers) releaseTouch(ctx context.Context, serial string) {
	// The CANCEL must come after the killed helpers have exited: an injection
	// still in flight after it would leave the pointer down again.
	_, _ = d.adb(ctx, serial, "shell", releaseTouchScript)
}

// The bracketed patterns ([c]om, [A]ptevaInput) keep pkill/pgrep from
// matching this script's own shell, whose command line contains the names.
// SIGTERM only: toybox "pkill -9 -f" also kills the calling shell.
const releaseTouchScript = `pkill -f '[c]om.android.commands.input'; pkill -f '[A]ptevaInput'; ` +
	`for i in 1 2 3 4 5 6 7 8 9 10; do pgrep -f '[A]ptevaInput' >/dev/null || pgrep -f '[c]om.android.commands.input' >/dev/null || break; sleep 0.1; done; ` +
	`input motionevent CANCEL 0 0 >/dev/null 2>&1; echo released`
