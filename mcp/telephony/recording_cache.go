package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

type recordingCacheEntry struct {
	path    string
	size    int64
	expires time.Time
}

var playbackCache = struct {
	sync.Mutex
	entries map[string]recordingCacheEntry
	pending map[string]chan struct{}
	slots   chan struct{}
}{entries: map[string]recordingCacheEntry{}, pending: map[string]chan struct{}{}, slots: make(chan struct{}, 2)}

const playbackCacheBytes int64 = 256 << 20

// Open under the cache lock: eviction can unlink an open file safely, while
// every request still performs the project's authorization and tombstone check.
func cachedRecording(ctx context.Context, key string, fetch func() (string, error)) (*os.File, error) {
	for {
		playbackCache.Lock()
		var size int64
		now := time.Now()
		for k, e := range playbackCache.entries {
			if now.After(e.expires) {
				_ = os.Remove(e.path)
				delete(playbackCache.entries, k)
			} else {
				size += e.size
			}
		}
		if e, ok := playbackCache.entries[key]; ok {
			f, err := os.Open(e.path)
			playbackCache.Unlock()
			return f, err
		}
		if wait, ok := playbackCache.pending[key]; ok {
			playbackCache.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		playbackCache.pending[key] = done
		playbackCache.Unlock()
		defer func() { playbackCache.Lock(); delete(playbackCache.pending, key); close(done); playbackCache.Unlock() }()
		select {
		case playbackCache.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		path, err := fetch()
		<-playbackCache.slots
		if err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			os.Remove(path)
			return nil, err
		}
		if ctx.Err() != nil {
			file.Close()
			os.Remove(path)
			return nil, ctx.Err()
		}
		if info.Size() > playbackCacheBytes {
			file.Close()
			os.Remove(path)
			return nil, errors.New("recording variant exceeds playback cache capacity; play the stored original")
		}
		playbackCache.Lock()
		size = 0
		for _, e := range playbackCache.entries {
			size += e.size
		}
		for k, e := range playbackCache.entries {
			if size+info.Size() <= playbackCacheBytes {
				break
			}
			_ = os.Remove(e.path)
			delete(playbackCache.entries, k)
			size -= e.size
		}
		playbackCache.entries[key] = recordingCacheEntry{path: path, size: info.Size(), expires: now.Add(10 * time.Minute)}
		playbackCache.Unlock()
		return file, nil
	}
}

type playbackDownloadContextKey struct{}

func recordingDownloadLimit(ctx context.Context) int64 {
	if value, ok := ctx.Value(playbackDownloadContextKey{}).(int64); ok && value > 0 {
		return value
	}
	return maxProviderRecordingBytes
}
