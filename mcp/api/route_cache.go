package main

import (
	"database/sql"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type compiledRoute struct {
	route    *APIRoute
	segments []string
}
type routeSnapshot struct {
	routes  []compiledRoute
	expires time.Time
}
type routeCache struct {
	mu        sync.RWMutex
	snapshots map[routeCacheKey]routeSnapshot
}
type routeCacheKey struct {
	project string
	apiID   int64
}

var routeCaches sync.Map

func cacheFor(db *sql.DB) *routeCache {
	if c, ok := routeCaches.Load(db); ok {
		return c.(*routeCache)
	}
	c, _ := routeCaches.LoadOrStore(db, &routeCache{snapshots: make(map[routeCacheKey]routeSnapshot)})
	return c.(*routeCache)
}
func releaseRouteCache(db *sql.DB) { routeCaches.Delete(db) }
func invalidateRoutes(db *sql.DB, pid string, id int64) {
	c := cacheFor(db)
	c.mu.Lock()
	delete(c.snapshots, routeCacheKey{pid, id})
	c.mu.Unlock()
}
func compiledRoutes(db *sql.DB, pid string, id int64) ([]compiledRoute, error) {
	c := cacheFor(db)
	key := routeCacheKey{pid, id}
	now := time.Now()
	c.mu.RLock()
	snapshot, ok := c.snapshots[key]
	c.mu.RUnlock()
	if ok && now.Before(snapshot.expires) {
		return snapshot.routes, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot, ok = c.snapshots[key]; ok && now.Before(snapshot.expires) {
		return snapshot.routes, nil
	}
	rows, err := dbListRoutes(db, pid, id)
	if err != nil {
		return nil, err
	}
	routes := make([]compiledRoute, 0, len(rows))
	for _, r := range rows {
		if r.Enabled {
			routes = append(routes, compiledRoute{r, splitPath(r.PathPattern)})
		}
	}
	sort.SliceStable(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if a.route.Priority != b.route.Priority {
			return a.route.Priority < b.route.Priority
		}
		for k := 0; k < len(a.segments) && k < len(b.segments); k++ {
			x, y := segmentRank(a.segments[k]), segmentRank(b.segments[k])
			if x != y {
				return x > y
			}
		}
		if len(a.segments) != len(b.segments) {
			// An exact path beats a catch-all matching an empty suffix.
			if len(a.segments) > len(b.segments) && a.segments[len(b.segments)] == "*" {
				return false
			}
			if len(b.segments) > len(a.segments) && b.segments[len(a.segments)] == "*" {
				return true
			}
			return len(a.segments) > len(b.segments)
		}
		if (a.route.Method == "ANY") != (b.route.Method == "ANY") {
			return a.route.Method != "ANY"
		}
		return a.route.ID < b.route.ID
	})
	if len(c.snapshots) >= 1024 {
		clear(c.snapshots)
	}
	c.snapshots[key] = routeSnapshot{routes, now.Add(time.Minute)}
	return routes, nil
}
func segmentRank(s string) int {
	if s == "*" {
		return 0
	}
	if strings.HasPrefix(s, ":") {
		return 1
	}
	return 2
}
func matchSegments(pattern, path []string) (map[string]string, bool) {
	// Avoid allocating parameter maps for candidates that do not match.
	for i, seg := range pattern {
		if seg == "*" {
			return captureParams(pattern, path), true
		}
		if i >= len(path) {
			return nil, false
		}
		if !strings.HasPrefix(seg, ":") && seg != path[i] {
			return nil, false
		}
	}
	if len(pattern) != len(path) {
		return nil, false
	}
	return captureParams(pattern, path), true
}
func captureParams(pattern, path []string) map[string]string {
	var out map[string]string
	for i, seg := range pattern {
		if strings.HasPrefix(seg, ":") || seg == "*" {
			if out == nil {
				out = make(map[string]string)
			}
			if seg == "*" {
				out["*"] = strings.Join(path[i:], "/")
				break
			}
			out[seg[1:]], _ = url.PathUnescape(path[i])
		}
	}
	return out
}
