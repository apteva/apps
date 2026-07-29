package main

import (
	"fmt"
	"sync"
	"time"
)

type usageDelta struct {
	Requests int64
	BytesIn  int64
	BytesOut int64
	LastAt   string
}

type usageBuffer struct {
	store    *store
	mu       sync.Mutex
	pending  map[string]usageDelta
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newUsageBuffer(store *store) *usageBuffer {
	return &usageBuffer{
		store:   store,
		pending: map[string]usageDelta{},
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (u *usageBuffer) record(tunnelID string, bytesIn, bytesOut int64) {
	if u == nil || tunnelID == "" {
		return
	}
	u.mu.Lock()
	delta := u.pending[tunnelID]
	delta.Requests++
	delta.BytesIn += bytesIn
	delta.BytesOut += bytesOut
	delta.LastAt = time.Now().UTC().Format(time.RFC3339Nano)
	u.pending[tunnelID] = delta
	u.mu.Unlock()
}

func (u *usageBuffer) run() {
	defer close(u.doneCh)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = u.flush()
		case <-u.stopCh:
			_ = u.flush()
			return
		}
	}
}

func (u *usageBuffer) stop() {
	if u == nil {
		return
	}
	u.stopOnce.Do(func() { close(u.stopCh) })
	<-u.doneCh
}

func (u *usageBuffer) flush() error {
	if u == nil || u.store == nil || u.store.db == nil {
		return nil
	}
	u.mu.Lock()
	if len(u.pending) == 0 {
		u.mu.Unlock()
		return nil
	}
	batch := u.pending
	u.pending = map[string]usageDelta{}
	u.mu.Unlock()

	tx, err := u.store.db.Begin()
	if err == nil {
		for tunnelID, delta := range batch {
			_, err = tx.Exec(`
				UPDATE tunnels
				SET request_count = request_count + ?,
				    bytes_in = bytes_in + ?,
				    bytes_out = bytes_out + ?,
				    last_request_at = ?,
				    updated_at = ?
				WHERE id = ?`,
				delta.Requests, delta.BytesIn, delta.BytesOut,
				delta.LastAt, delta.LastAt, tunnelID,
			)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		u.requeue(batch)
		return fmt.Errorf("flush tunnel usage: %w", err)
	}
	return nil
}

func (u *usageBuffer) requeue(batch map[string]usageDelta) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for tunnelID, failed := range batch {
		current := u.pending[tunnelID]
		current.Requests += failed.Requests
		current.BytesIn += failed.BytesIn
		current.BytesOut += failed.BytesOut
		if current.LastAt == "" || failed.LastAt > current.LastAt {
			current.LastAt = failed.LastAt
		}
		u.pending[tunnelID] = current
	}
}
