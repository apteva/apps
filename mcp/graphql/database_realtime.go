package main

// Database subscriptions. A Subscription field bound to a Database read
// resolver re-runs whenever Database's native change feed (db_changes) reports
// a commit touching the watched collections. Frames are invalidations without
// row data, which matches the hub's re-execute-on-event model. One watcher per
// project, scope, database and collection set is shared by every subscriber:
// Database admits at most 8 concurrent watches per calling app.
import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	databaseWatchWaitMS       = 20000
	databaseWatchReadyTimeout = 10 * time.Second
	databaseWatchMaxBackoff   = 30 * time.Second
)

type databaseWatchSpec struct {
	project, database, scope string
	collections              []string
}

// topic is the hub topic watchers publish to. Database and collection names
// are validated identifiers, so ":" and "," cannot appear inside them.
func (s databaseWatchSpec) topic() string {
	return "database:" + s.scope + ":" + s.database + ":" + strings.Join(s.collections, ",")
}

func (s databaseWatchSpec) key() string { return s.project + "\x00" + s.topic() }

// databaseWatchFor derives the change-feed watch for a Subscription resolver.
// watch_collections overrides the source collection, for example to re-run an
// aggregate when a related collection changes.
func databaseWatchFor(project string, source sourceRecord, resolver resolverRecord) (databaseWatchSpec, bool) {
	switch strings.ToLower(strings.TrimSpace(resolver.Operation)) {
	case "find", "list", "search", "get", "count", "aggregate":
	default:
		return databaseWatchSpec{}, false
	}
	config := mergeMaps(source.Config, resolver.Config)
	seen := map[string]bool{}
	collections := []string{}
	add := func(value any) {
		if name, ok := value.(string); ok && strings.TrimSpace(name) != "" && !seen[strings.TrimSpace(name)] {
			seen[strings.TrimSpace(name)] = true
			collections = append(collections, strings.TrimSpace(name))
		}
	}
	if list, ok := config["watch_collections"].([]any); ok {
		for _, value := range list {
			add(value)
		}
	}
	if len(collections) == 0 {
		add(config["collection"])
	}
	if len(collections) == 0 {
		return databaseWatchSpec{}, false
	}
	sort.Strings(collections)
	return databaseWatchSpec{
		project:     project,
		database:    stringValue(config["database"], "default"),
		scope:       stringValue(config["scope"], "project"),
		collections: collections,
	}, true
}

type databaseWatcher struct {
	spec   databaseWatchSpec
	refs   int
	cancel context.CancelFunc
	ready  chan struct{}
	err    error // set before ready closes when the watch could not open
}

type databaseChanges struct {
	Events []struct {
		Type        string   `json:"type"`
		Collections []string `json:"collections"`
	} `json:"events"`
	Cursor string `json:"cursor"`
}

// acquireDatabaseWatch joins (or starts) the shared watcher and returns once
// it holds a change-feed boundary, so a subscriber that then runs its initial
// query cannot miss a commit made after that query.
func (a *App) acquireDatabaseWatch(ctx context.Context, spec databaseWatchSpec) (func(), error) {
	key := spec.key()
	a.dbWatchMu.Lock()
	if a.dbWatches == nil {
		a.dbWatches = map[string]*databaseWatcher{}
	}
	w := a.dbWatches[key]
	if w == nil {
		watchCtx, cancel := context.WithCancel(context.Background())
		w = &databaseWatcher{spec: spec, cancel: cancel, ready: make(chan struct{})}
		a.dbWatches[key] = w
		go a.runDatabaseWatch(watchCtx, w)
	}
	w.refs++
	a.dbWatchMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			a.dbWatchMu.Lock()
			defer a.dbWatchMu.Unlock()
			w.refs--
			if w.refs == 0 {
				w.cancel()
				if a.dbWatches[key] == w {
					delete(a.dbWatches, key)
				}
			}
		})
	}
	timer := time.NewTimer(databaseWatchReadyTimeout)
	defer timer.Stop()
	select {
	case <-w.ready:
		if w.err != nil {
			release()
			return nil, w.err
		}
		return release, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-timer.C:
		release()
		return nil, runtimeError("database_watch_unavailable", "database change feed did not open in time")
	}
}

func (a *App) runDatabaseWatch(ctx context.Context, w *databaseWatcher) {
	spec := w.spec
	cursor, ready, resync := "", false, false
	backoff := time.Duration(0)
	for ctx.Err() == nil {
		input := map[string]any{"database": spec.database, "collections": spec.collections, "scope": spec.scope, "_project_id": spec.project, "waitMs": databaseWatchWaitMS}
		if cursor == "" {
			// Opening (or reopening) only needs the current boundary.
			input["waitMs"] = 0
		} else {
			input["after"] = cursor
		}
		var out databaseChanges
		err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(spec.project).PlatformAPI(), "database", "db_changes", input, &out)
		if ctx.Err() != nil {
			if !ready {
				w.err = ctx.Err()
				close(w.ready)
			}
			return
		}
		if err != nil {
			if !ready {
				a.forgetDatabaseWatch(w)
				w.err = err
				close(w.ready)
				return
			}
			if cursor != "" && (strings.Contains(err.Error(), "history_expired") || strings.Contains(err.Error(), "resume_invalid")) {
				// Commits may have been lost: reopen, then re-run every subscriber.
				cursor, resync = "", true
				continue
			}
			backoff = min(max(2*backoff, time.Second), databaseWatchMaxBackoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0
		if out.Cursor != "" {
			cursor = out.Cursor
		}
		if !ready {
			ready = true
			close(w.ready)
		}
		if resync {
			resync = false
			a.publishDatabaseChange(spec, spec.collections)
		}
		for _, event := range out.Events {
			if event.Type == "invalidate" {
				a.publishDatabaseChange(spec, event.Collections)
			}
		}
	}
}

func (a *App) forgetDatabaseWatch(w *databaseWatcher) {
	a.dbWatchMu.Lock()
	defer a.dbWatchMu.Unlock()
	if a.dbWatches[w.spec.key()] == w {
		delete(a.dbWatches, w.spec.key())
	}
}

func (a *App) publishDatabaseChange(spec databaseWatchSpec, collections []string) {
	if a.hub == nil {
		return
	}
	a.hub.publishAllAPIs(spec.project, spec.topic(), map[string]any{"database": spec.database, "collections": collections})
}

func (a *App) stopDatabaseWatches() {
	a.dbWatchMu.Lock()
	defer a.dbWatchMu.Unlock()
	for key, w := range a.dbWatches {
		w.cancel()
		delete(a.dbWatches, key)
	}
}
