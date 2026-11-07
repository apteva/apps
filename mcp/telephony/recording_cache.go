package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

type recordingCacheEntry struct {
	path            string
	size            int64
	expires         time.Time
	readers         int
	temporary       bool
	expirationTimer *time.Timer
}

var playbackCache = struct {
	sync.Mutex
	entries map[string]*recordingCacheEntry
	pending map[string]chan struct{}
	slots   chan struct{}
}{entries: map[string]*recordingCacheEntry{}, pending: map[string]chan struct{}{}, slots: make(chan struct{}, 2)}

const playbackCacheBytes int64 = 256 << 20
const playbackCacheEntries = 128
const playbackCacheTTL = 10 * time.Minute

// The two disk-work permits cover imports, downloads and generated playback.
// Large files hold a permit until the last reader closes. At most two jobs can
// each retain a source and output: WAV work up to 2 GiB/job, MP3 decoding
// up to 2 GiB PCM plus 1 GiB mono output (6 GiB total for two jobs), in
// addition to the 256 MiB reusable cache. Durable originals are never cache files.
type recordingPlaybackFile struct {
	*os.File
	once    sync.Once
	release func()
}

func (f *recordingPlaybackFile) Close() error {
	var err error
	f.once.Do(func() {
		err = f.File.Close()
		if f.release != nil {
			f.release()
		}
	})
	return err
}
func removeRecordingCacheEntry(key string, e *recordingCacheEntry) {
	if playbackCache.entries[key] != e || e.readers != 0 {
		return
	}
	if e.expirationTimer != nil {
		e.expirationTimer.Stop()
	}
	delete(playbackCache.entries, key)
	_ = os.Remove(e.path)
	if e.temporary {
		<-playbackCache.slots
	}
}
func openRecordingCacheEntry(key string, e *recordingCacheEntry) (*recordingPlaybackFile, error) {
	file, err := os.Open(e.path)
	if err != nil {
		removeRecordingCacheEntry(key, e)
		return nil, err
	}
	e.readers++
	return &recordingPlaybackFile{File: file, release: func() {
		playbackCache.Lock()
		defer playbackCache.Unlock()
		e.readers--
		if e.readers == 0 && (e.temporary || time.Now().After(e.expires)) {
			removeRecordingCacheEntry(key, e)
		}
	}}, nil
}
func expireRecordingCacheEntry(key string, e *recordingCacheEntry) {
	playbackCache.Lock()
	defer playbackCache.Unlock()
	if time.Now().After(e.expires) {
		removeRecordingCacheEntry(key, e)
	}
}
func cachedRecording(ctx context.Context, key string, fetch func() (string, error)) (*recordingPlaybackFile, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		playbackCache.Lock()
		for k, e := range playbackCache.entries {
			if time.Now().After(e.expires) && !e.temporary {
				removeRecordingCacheEntry(k, e)
			}
		}
		if e, ok := playbackCache.entries[key]; ok {
			f, err := openRecordingCacheEntry(key, e)
			missing := playbackCache.entries[key] != e && os.IsNotExist(err)
			playbackCache.Unlock()
			if missing {
				continue
			}
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
		held := true
		defer func() {
			if held {
				<-playbackCache.slots
			}
		}()
		path, err := fetch()
		if err != nil {
			if path != "" {
				_ = os.Remove(path)
			}
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.Mode().IsRegular() {
			_ = os.Remove(path)
			return nil, errors.New("invalid playback temporary file")
		}
		if err := ctx.Err(); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		if info.Size() > min(recordingDownloadLimit(ctx), maxRecordingFileBytes) {
			_ = os.Remove(path)
			return nil, &recordingSizeLimitError{min(recordingDownloadLimit(ctx), maxRecordingFileBytes)}
		}
		e := &recordingCacheEntry{path: path, size: info.Size(), expires: time.Now().Add(playbackCacheTTL)}
		playbackCache.Lock()
		var size int64
		count := 0
		for _, entry := range playbackCache.entries {
			if !entry.temporary {
				size += entry.size
				count++
			}
		}
		for k, entry := range playbackCache.entries {
			if size+e.size <= playbackCacheBytes && count < playbackCacheEntries {
				break
			}
			if entry.temporary || entry.readers > 0 {
				continue
			}
			removeRecordingCacheEntry(k, entry)
			size -= entry.size
			count--
		}
		e.temporary = size+e.size > playbackCacheBytes || count >= playbackCacheEntries
		playbackCache.entries[key] = e
		result, err := openRecordingCacheEntry(key, e)
		if e.temporary {
			held = false
		} // transferred to the entry, released by last close
		if !e.temporary && err == nil {
			e.expirationTimer = time.AfterFunc(playbackCacheTTL, func() { expireRecordingCacheEntry(key, e) })
		}
		playbackCache.Unlock()
		return result, err
	}
}

type playbackDownloadContextKey struct{}

func recordingDownloadLimit(ctx context.Context) int64 {
	if value, ok := ctx.Value(playbackDownloadContextKey{}).(int64); ok {
		if value <= 0 {
			return maxRecordingFileBytes
		}
		return min(value, maxRecordingFileBytes)
	}
	return maxProviderRecordingBytes
}
