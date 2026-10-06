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

// playbackMissTTL bounds how long a "no such stream" answer is reused.
//
// v0.2 cached hits only, so every request naming an id that doesn't
// exist went to the DB. The media and heartbeat routes are NoAuth and
// take the id straight off the path, so anyone could loop
// GET /streams/999999/index.m3u8 and queue serialized reads ahead of
// every real viewer's manifest fetch on the single-connection app DB —
// the exact contention this cache exists to remove, reachable without
// a token. Misses are cached briefly; streams_create invalidates so a
// freshly minted id is never shadowed by a probe that preceded it.
const playbackMissTTL = 5 * time.Second

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
	// missing marks a cached negative: the row did not exist when we
	// looked. Held for playbackMissTTL rather than the full ttl.
	missing bool
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

// get returns the cached entry for one stream. `fresh` reports whether
// anything usable was found; `missing` distinguishes a cached negative
// (the row does not exist) from a cached record.
func (c *playbackCache) get(pid string, id int64) (rec playbackRecord, missing, fresh bool) {
	if c == nil {
		return playbackRecord{}, false, false
	}
	c.mu.RLock()
	e, ok := c.entries[playbackCacheKey(pid, id)]
	c.mu.RUnlock()
	if !ok {
		return playbackRecord{}, false, false
	}
	ttl := c.ttl
	if e.missing {
		ttl = playbackMissTTL
	}
	if time.Since(e.fetchedAt) > ttl {
		return playbackRecord{}, false, false
	}
	return e.rec, e.missing, true
}

func (c *playbackCache) put(rec playbackRecord) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[playbackCacheKey(rec.ProjectID, rec.ID)] = playbackEntry{rec: rec, fetchedAt: time.Now()}
	c.mu.Unlock()
}

// putMissing records that (pid, id) named no row.
func (c *playbackCache) putMissing(pid string, id int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[playbackCacheKey(pid, id)] = playbackEntry{fetchedAt: time.Now(), missing: true}
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
	if rec, missing, fresh := a.playback.get(pid, id); fresh {
		if missing {
			return nil, nil
		}
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
			a.playback.putMissing(pid, id)
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
