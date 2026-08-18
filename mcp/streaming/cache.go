package main

import (
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// playbackRecord is the slice of a stream row the media + heartbeat
// handlers actually gate on. Everything in it is either immutable for
// the life of the stream (tokens, secret, storage prefix) or changes
// only on a path that invalidates the cache explicitly (status flip,
// key rotation, policy change, delete).
type playbackRecord struct {
	ID                int64
	ProjectID         string
	Visibility        string
	PlaybackToken     string
	SigningSecret     string
	RequireSignedURLs bool
	StoragePrefix     string
	Status            string
	Record            bool
}

// playbackCacheTTL is a backstop, not the invalidation mechanism —
// every mutating path calls invalidatePlayback. It bounds the damage
// from a future path that forgets to.
const playbackCacheTTL = 10 * time.Second

// playbackCache keeps one playbackRecord per (project, stream).
//
// Why this exists: the SDK opens the app DB with SetMaxOpenConns(1),
// so every read AND write serializes through a single connection.
// v0.1 did a full 26-column row read on EVERY manifest and segment
// request — at 1000 viewers on 4s segments that is ~750 serialized
// reads/sec gating media delivery, queued behind the watchdog's
// per-stream UPDATEs. It also meant the built-in load test spent its
// own measurement window contending with the thing it was measuring.
type playbackCache struct {
	mu      sync.RWMutex
	entries map[string]playbackEntry
	ttl     time.Duration
}

type playbackEntry struct {
	rec       playbackRecord
	fetchedAt time.Time
}

func newPlaybackCache(ttl time.Duration) *playbackCache {
	if ttl <= 0 {
		ttl = playbackCacheTTL
	}
	return &playbackCache{entries: map[string]playbackEntry{}, ttl: ttl}
}

func playbackCacheKey(pid string, id int64) string {
	return pid + ":" + strconv.FormatInt(id, 10)
}

func (c *playbackCache) get(pid string, id int64) (playbackRecord, bool) {
	if c == nil {
		return playbackRecord{}, false
	}
	c.mu.RLock()
	e, ok := c.entries[playbackCacheKey(pid, id)]
	c.mu.RUnlock()
	if !ok || time.Since(e.fetchedAt) > c.ttl {
		return playbackRecord{}, false
	}
	return e.rec, true
}

func (c *playbackCache) put(rec playbackRecord) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[playbackCacheKey(rec.ProjectID, rec.ID)] = playbackEntry{rec: rec, fetchedAt: time.Now()}
	c.mu.Unlock()
}

func (c *playbackCache) invalidate(pid string, id int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, playbackCacheKey(pid, id))
	c.mu.Unlock()
}

// playbackFor returns the gating record for one stream, from cache
// when it's warm. Returns (nil, nil) when the row doesn't exist.
func (a *App) playbackFor(ctx *sdk.AppCtx, pid string, id int64) (*playbackRecord, error) {
	if rec, ok := a.playback.get(pid, id); ok {
		return &rec, nil
	}
	rec := playbackRecord{ID: id}
	var record, requireSigned int
	err := ctx.AppDB().QueryRow(
		`SELECT project_id, visibility, playback_token,
				COALESCE(url_signing_secret,''), require_signed_urls,
				storage_prefix, status, record
		 FROM streams WHERE id = ? AND project_id = ?`,
		id, pid).Scan(
		&rec.ProjectID, &rec.Visibility, &rec.PlaybackToken,
		&rec.SigningSecret, &requireSigned,
		&rec.StoragePrefix, &rec.Status, &record)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.Record = record != 0
	rec.RequireSignedURLs = requireSigned != 0
	a.playback.put(rec)
	return &rec, nil
}

// invalidatePlayback drops one stream's cached gating record. Call it
// from every path that writes status, tokens, the signing secret, the
// URL policy, or the storage prefix.
func (a *App) invalidatePlayback(pid string, id int64) {
	a.playback.invalidate(pid, id)
}
