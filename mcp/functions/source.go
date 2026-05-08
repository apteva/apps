package main

import (
	"errors"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// resolveSource returns the function body bytes for one invocation.
//
// inline — the bytes already live on the function row.
// repo   — fetch via CallAppResult("code","code_read_file",...) per
//          the project's cross-app convention. Cached in-memory by
//          (repo_id, repo_path, source_hash); the hash key keeps the
//          cache correct across functions_update without us needing
//          to plumb invalidation calls.
//
// On cache miss we re-fetch and overwrite. On miss without a stored
// hash we still fetch but skip caching — first call after a fresh
// boot pays the round-trip, subsequent calls reuse.
func resolveSource(ctx *sdk.AppCtx, fn *Function) ([]byte, error) {
	if fn.SourceKind == "inline" {
		return []byte(fn.Source), nil
	}
	if fn.RepoID == nil || fn.RepoPath == "" {
		return nil, errors.New("repo source missing repo_id or repo_path")
	}
	if cached, ok := sourceCache.get(*fn.RepoID, fn.RepoPath, fn.SourceHash); ok {
		return cached, nil
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("repo source requires PlatformAPI; not available in this context")
	}
	// code_read_file returns {"path","content","line_count","..."}.
	// We only care about content.
	var resp struct {
		Content string `json:"content"`
	}
	if err := ctx.PlatformAPI().CallAppResult("code", "code_read_file", map[string]any{
		"repo_id": *fn.RepoID,
		"path":    fn.RepoPath,
	}, &resp); err != nil {
		return nil, err
	}
	bytes := []byte(resp.Content)
	if fn.SourceHash != "" {
		sourceCache.put(*fn.RepoID, fn.RepoPath, fn.SourceHash, bytes)
	}
	return bytes, nil
}

// repoSourceCache memoises repo-fetched function bodies. The key
// includes source_hash so that an update which doesn't change bytes
// (re-saving the same file) still validates against existing cache,
// while an update that does change bytes evicts the previous entry
// at next read.
//
// In-memory only. A sidecar restart cold-starts the cache, which is
// fine — we'd just re-fetch on first invocation.
type repoSourceCache struct {
	mu sync.RWMutex
	m  map[repoKey]repoEntry
}

type repoKey struct {
	repoID int64
	path   string
}

type repoEntry struct {
	hash  string
	bytes []byte
}

func (c *repoSourceCache) get(repoID int64, path, hash string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[repoKey{repoID, path}]
	if !ok || e.hash != hash {
		return nil, false
	}
	return e.bytes, true
}

func (c *repoSourceCache) put(repoID int64, path, hash string, bytes []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[repoKey]repoEntry{}
	}
	c.m[repoKey{repoID, path}] = repoEntry{hash, bytes}
}

var sourceCache = &repoSourceCache{}
