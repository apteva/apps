package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type dashboardExtraEntry struct {
	value   any
	expires time.Time
	ready   chan struct{}
	err     error
}
type dashboardExtrasCache struct {
	mu      sync.Mutex
	entries map[string]*dashboardExtraEntry
}

// Cache only aggregate/filter computations. Live rows/counters and access checks
// are always fresh. Durable epochs invalidate this bounded one-second cache.
func (c *callsDB) dashboardExtra(ctx context.Context, project string, f audioDashboardFilter, now time.Time, kind string, cache *dashboardExtrasCache, load func() (any, error)) (any, error) {
	if cache == nil {
		return load()
	}
	var audio, reads int64
	err := c.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM telephony_audio_versions WHERE project_id=?),0),COALESCE((SELECT revision FROM telephony_read_versions WHERE project_id=?),0)`, project, project).Scan(&audio, &reads)
	if err != nil {
		return nil, err
	}
	f.Cursor = ""
	f.Limit = 0
	f.OmitTotals = false
	f.OmitFacets = false
	raw, _ := json.Marshal(struct {
		Project, Kind string
		Filter        audioDashboardFilter
		Audio, Reads  int64
	}{project, kind, f, audio, reads})
	key := string(raw)
	cache.mu.Lock()
	if e := cache.entries[key]; e != nil && now.Before(e.expires) {
		ready := e.ready
		cache.mu.Unlock()
		if ready != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ready:
			}
		}
		return e.value, e.err
	}
	if cache.entries == nil || len(cache.entries) >= 128 {
		cache.entries = make(map[string]*dashboardExtraEntry)
	}
	e := &dashboardExtraEntry{expires: now.Add(time.Second), ready: make(chan struct{})}
	cache.entries[key] = e
	cache.mu.Unlock()
	value, err := load()
	cache.mu.Lock()
	e.value, e.err = value, err
	close(e.ready)
	e.ready = nil
	if err != nil && cache.entries[key] == e {
		delete(cache.entries, key)
	}
	cache.mu.Unlock()
	return value, err
}
