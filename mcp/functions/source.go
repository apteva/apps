package main

import (
	"errors"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// resolveVersionSource returns the function body bytes for one
// version.
//
// inline — the bytes already live on the version row.
// repo   — fetch via CallAppResult("code","code_read_file",...) per
//          the project's cross-app convention. Cached in-memory by
//          (repo_id, repo_path, source_hash) so a fresh boot pays the
//          round-trip once and subsequent reads reuse.
func resolveVersionSource(ctx *sdk.AppCtx, v *FunctionVersion) ([]byte, error) {
	if v.SourceKind == "inline" {
		return []byte(v.Source), nil
	}
	if v.RepoID == nil || v.RepoPath == "" {
		return nil, errors.New("repo source missing repo_id or repo_path")
	}
	if cached, ok := sourceCache.get(*v.RepoID, v.RepoPath, v.SourceHash); ok {
		return cached, nil
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("repo source requires PlatformAPI; not available in this context")
	}
	// code_read_file returns {"path","content","line_count",...}.
	var resp struct {
		Content string `json:"content"`
	}
	if err := ctx.PlatformAPI().CallAppResult("code", "code_read_file", map[string]any{
		"repo_id": *v.RepoID,
		"path":    v.RepoPath,
	}, &resp); err != nil {
		return nil, err
	}
	bytes := []byte(resp.Content)
	if v.SourceHash != "" {
		sourceCache.put(*v.RepoID, v.RepoPath, v.SourceHash, bytes)
	}
	return bytes, nil
}

// repoSourceCache memoises repo-fetched function bodies. The key
// includes source_hash so a deploy that changes bytes naturally
// misses and re-fetches, while one that re-uses identical bytes hits.
//
// In-memory only. A sidecar restart cold-starts the cache, which is
// fine — first invocation re-fetches.
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
