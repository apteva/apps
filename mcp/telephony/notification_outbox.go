package main

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

type callNotification struct {
	id, project, call, topic, at string
	payload                      map[string]any
}
type callNotificationQueue struct {
	mu      sync.Mutex
	flushMu sync.Mutex
	pending []callNotification
	dropped atomic.Uint64
}

// Audio observers append only bounded memory. Durable persistence and HTTP
// retries run in the existing background worker, never on an RTP/socket loop.
// Observers never block: on lock contention or a full queue the event is
// counted as dropped.
func (a *App) observeCallNotification(project, call, topic string, payload map[string]any) {
	q := &a.notifications
	if !q.mu.TryLock() {
		q.dropped.Add(1)
		return
	}
	defer q.mu.Unlock()
	if len(q.pending) >= callNotificationObserverLimit {
		q.dropped.Add(1)
		return
	}
	q.pending = append(q.pending, newCallNotification(project, call, topic, payload))
}

const (
	callNotificationObserverLimit = 512
	// Durable events (routing, recording.ready) are low-rate and promised to
	// persist, so they get headroom past the observer limit before dropping.
	callNotificationDurableLimit = 4096
)

func newCallNotification(project, call, topic string, payload map[string]any) callNotification {
	return callNotification{id: "notification_" + newCallID(), project: project, call: call, topic: topic, at: ringTime(time.Now()), payload: payload}
}

// saveCallNotification is the durable path. Routing and recording callbacks
// are not media loops, so it takes the locks (blocking) and flushes until the
// event is committed, so a restart cannot lose an acknowledged update.
func (a *App) saveCallNotification(project, call, topic string, payload map[string]any) {
	q := &a.notifications
	v := newCallNotification(project, call, topic, payload)
	q.mu.Lock()
	if len(q.pending) >= callNotificationDurableLimit {
		q.mu.Unlock()
		q.dropped.Add(1)
		if globalCtx != nil {
			globalCtx.Logger().Warn("call notification queue full; durable event dropped", "call", call, "topic", topic)
		}
		return
	}
	q.pending = append(q.pending, v)
	q.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q.flushMu.Lock()
	defer q.flushMu.Unlock()
	for ctx.Err() == nil && q.isPending(v.id) {
		if err := a.flushCallNotificationsLocked(ctx); err != nil {
			if globalCtx != nil {
				globalCtx.Logger().Warn("persist call notifications", "err", err)
			}
			return
		}
	}
}

func (q *callNotificationQueue) isPending(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, v := range q.pending {
		if v.id == id {
			return true
		}
	}
	return false
}

func (a *App) flushCallNotifications(ctx context.Context) error {
	q := &a.notifications
	if !q.flushMu.TryLock() {
		return nil
	}
	defer q.flushMu.Unlock()
	return a.flushCallNotificationsLocked(ctx)
}

// flushCallNotificationsLocked persists up to 100 pending events. The caller
// holds q.flushMu.
func (a *App) flushCallNotificationsLocked(ctx context.Context) error {
	q := &a.notifications
	q.mu.Lock()
	n := min(100, len(q.pending))
	batch := append([]callNotification(nil), q.pending[:n]...)
	q.mu.Unlock()
	if n == 0 {
		return nil
	}
	tx, err := a.db().db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range batch {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM calls WHERE id=? AND project_id=?`, v.call, v.project).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE calls SET lifecycle_revision=lifecycle_revision+1 WHERE id=?`, v.call); err != nil {
			return err
		}
		var revision int64
		if err = tx.QueryRowContext(ctx, `SELECT lifecycle_revision FROM calls WHERE id=?`, v.call).Scan(&revision); err != nil {
			return err
		}
		payload := map[string]any{}
		for k, value := range v.payload {
			payload[k] = value
		}
		payload["event_id"] = v.id
		payload["revision"] = revision
		payload["call_id"] = v.call
		payload["project_id"] = v.project
		if _, ok := payload["occurred_at"]; !ok {
			payload["occurred_at"] = v.at
		}
		raw, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, v.id, v.call, v.project, v.topic, revision, v.at, string(raw), v.at); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	q.mu.Lock()
	q.pending = append(q.pending[:0], q.pending[n:]...)
	q.mu.Unlock()
	for _, v := range batch {
		a.wakeRouting(v.project)
	}
	return nil
}
