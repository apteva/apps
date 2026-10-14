package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	native "github.com/apteva/db"
)

// RealtimeOptions is persisted per database. Disabling ends the native history.
// Retention is logical; recovery tails and WAL segments can exceed this target.
type RealtimeOptions struct {
	Enabled          bool  `json:"enabled"`
	RetentionBytes   int64 `json:"retentionBytes"`
	RetentionSeconds int   `json:"retentionSeconds"`
}

func nativeFeedOptions(o RealtimeOptions) *native.ChangeFeedOptions {
	return &native.ChangeFeedOptions{Disabled: !o.Enabled, RetentionBytes: o.RetentionBytes, RetentionAge: time.Duration(o.RetentionSeconds) * time.Second, MaxWatchers: 32, MaxWatchBytes: 32 << 20}
}
func normalizeRealtime(r Request) (RealtimeOptions, error) {
	if r.Enabled == nil {
		return RealtimeOptions{}, Invalid("enabled is required")
	}
	o := RealtimeOptions{*r.Enabled, r.RetentionBytes, r.RetentionSeconds}
	if o.RetentionBytes == 0 {
		o.RetentionBytes = 128 << 20
	}
	if o.RetentionSeconds == 0 {
		o.RetentionSeconds = 3600
	}
	if o.RetentionBytes < 1<<20 || o.RetentionBytes > 256<<20 || o.RetentionSeconds < 1 || o.RetentionSeconds > 86400 {
		return o, Invalid("retentionBytes must be 1–256 MiB and retentionSeconds 1–86400")
	}
	return o, nil
}
func (m *Manager) realtimeOperation(ctx context.Context, scope, op string, r Request) (any, error) {
	if scope == "" {
		return nil, fail("permission_denied", "authenticated project scope required")
	}
	if r.Database == "" {
		r.Database = "default"
	}
	d, e := m.resolve(ctx, scope, r.Database, "", false)
	if e != nil {
		return nil, e
	}
	if op == "realtime_describe" {
		d.mu.RLock()
		defer d.mu.RUnlock()
		if d.closed {
			return nil, fail("not_found", "database is closed")
		}
		b, ok := d.backend.(*aptevaBackend)
		out := map[string]any{"supported": ok, "enabled": false, "adapter": d.info.Adapter, "maxWatchers": 32, "maxProcessWatchers": 64, "maxCallerWatchers": 8}
		if ok {
			stats := b.db.ChangeFeedStats()
			out["enabled"] = stats.Enabled
			out["stats"] = stats
			out["policy"] = d.realtime
		}
		return out, nil
	}
	policy, e := normalizeRealtime(r)
	if e != nil {
		return nil, e
	}
	// Same lock order as database drop/manager shutdown. No feed waits hold these locks.
	m.mu.Lock()
	defer m.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.closed || d.closed {
		return nil, fail("not_found", "database is closed")
	}
	if _, ok := d.backend.(*aptevaBackend); !ok {
		return nil, fail("unsupported", "realtime capture currently requires aptevadb")
	}
	if policy.Enabled {
		rows, e := m.registry.QueryContext(ctx, `SELECT realtime.id,realtime.policy FROM realtime JOIN databases ON databases.id=realtime.id`)
		if e != nil {
			return nil, e
		}
		total := policy.RetentionBytes
		for rows.Next() {
			var id, p string
			if e = rows.Scan(&id, &p); e != nil {
				break
			}
			var o RealtimeOptions
			if e = decode([]byte(p), &o); e != nil {
				break
			}
			if id != d.info.ID && o.Enabled {
				total += o.RetentionBytes
			}
		}
		scanErr := rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if scanErr != nil {
			return nil, scanErr
		}
		if total > 1<<30 {
			return nil, fail("resource_limit", "configured realtime retention exceeds the app's 1 GiB logical budget")
		}
	}
	// Persist desired policy before reopening; a crash/reopen completes this policy.
	if _, e = m.registry.ExecContext(ctx, `INSERT INTO realtime(id,policy) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET policy=excluded.policy`, d.info.ID, string(marshal(policy))); e != nil {
		return nil, e
	}
	d.closeWatches()
	d.schemas.clear()
	if e = d.backend.close(); e != nil {
		d.closed = true
		return nil, e
	}
	options := m.apteva
	options.Realtime = &policy
	b, e := openAptevaDBWithOptions(filepath.Join(m.root, "databases", d.info.ID, "aptevadb"), options)
	if e != nil {
		d.closed = true
		return nil, e
	}
	d.backend = b
	d.realtime = &policy
	return map[string]any{"supported": true, "enabled": policy.Enabled, "policy": policy}, nil
}

type WatchEvent struct {
	Type        string   `json:"type"`
	Collections []string `json:"collections,omitempty"`
	Cursor      string   `json:"cursor"`
}
type watchToken struct {
	Version  int    `json:"v"`
	Database string `json:"d"`
	Spec     string `json:"s"`
	Native   string `json:"n"`
}
type Watch struct {
	cursor              *native.ChangeCursor
	database            *database
	manager             *Manager
	caller, fingerprint string
	selected            map[string]bool
	once                sync.Once
	Initial             any
	Ready               string
}

func (w *Watch) sign(token string) string {
	payload := marshal(watchToken{1, w.database.info.ID, w.fingerprint, token})
	mac := hmac.New(sha256.New, w.database.key)
	mac.Write([]byte("database-watch-v1\x00"))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (w *Watch) verify(token string) (string, error) {
	if len(token) > 8192 {
		return "", fail("resume_invalid", "invalid watch cursor")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", fail("resume_invalid", "invalid watch cursor")
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return "", fail("resume_invalid", "invalid watch cursor")
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "", fail("resume_invalid", "invalid watch cursor")
	}
	mac := hmac.New(sha256.New, w.database.key)
	mac.Write([]byte("database-watch-v1\x00"))
	mac.Write(payload)
	var t watchToken
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(payload, &t) != nil || t.Version != 1 || t.Database != w.database.info.ID || t.Spec != w.fingerprint || t.Native == "" {
		return "", fail("resume_invalid", "cursor belongs to a different database or subscription")
	}
	return t.Native, nil
}
func watchError(e error) error {
	switch {
	case errors.Is(e, native.ErrChangeFeedDisabled):
		return fail("realtime_disabled", "enable realtime capture first")
	case errors.Is(e, native.ErrHistoryExpired):
		return fail("history_expired", "load a fresh snapshot to resume")
	case errors.Is(e, native.ErrResumeInvalid):
		return fail("resume_invalid", "watch history was replaced; load a fresh snapshot")
	case errors.Is(e, native.ErrWatchLimit), errors.Is(e, native.ErrLimit):
		return fail("resource_limit", "watch resource budget exceeded")
	}
	return e
}
func (m *Manager) OpenWatch(ctx context.Context, scope string, r Request) (*Watch, error) {
	if scope == "" {
		return nil, fail("permission_denied", "authenticated project scope required")
	}
	if r.Database == "" {
		r.Database = "default"
	}
	if len(r.Collections) == 0 || len(r.Collections) > 16 {
		return nil, Invalid("watch requires 1–16 explicit collections")
	}
	selected := map[string]bool{}
	names := append([]string(nil), r.Collections...)
	for _, n := range names {
		if e := nameOK(n); e != nil {
			return nil, e
		}
		if selected[n] {
			return nil, Invalid("duplicate collection")
		}
		selected[n] = true
	}
	sort.Strings(names)
	initialTimeout := 30 * time.Second
	if r.Initial != nil {
		timeout, err := OperationTimeout(r.Initial.Op, r.Initial.Request.TimeoutMS)
		if err != nil {
			return nil, err
		}
		initialTimeout = min(initialTimeout, timeout)
		if r.After != "" {
			return nil, Invalid("initial snapshot and after are mutually exclusive")
		}
		if r.Initial.Op != "find" && r.Initial.Op != "count" && r.Initial.Op != "aggregate" {
			return nil, Invalid("initial supports find, count or aggregate")
		}
		initial := r.Initial.Request
		if !selected[initial.Collection] || initial.Cursor != "" || (initial.Database != "" && initial.Database != r.Database) {
			return nil, Invalid("initial must read a selected collection without a page cursor")
		}
	}
	d, e := m.resolve(ctx, scope, r.Database, "", false)
	if e != nil {
		return nil, e
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, fail("not_found", "database is closed")
	}
	b, ok := d.backend.(*aptevaBackend)
	if !ok {
		return nil, fail("unsupported", "realtime currently requires aptevadb")
	}
	w := &Watch{database: d, manager: m, caller: scope + "/" + r.WatchCaller, selected: selected}
	sum := sha256.Sum256(marshal(names))
	w.fingerprint = base64.RawURLEncoding.EncodeToString(sum[:])
	var after string
	if r.After != "" {
		after, e = w.verify(r.After)
		if e != nil {
			return nil, e
		}
	}
	// Reserve app quotas before opening a native cursor, rollback on any failure.
	m.watchMu.Lock()
	d.watchesMu.Lock()
	if m.watchCount >= 64 || len(d.watches) >= 32 || m.watchCallers[w.caller] >= 8 {
		d.watchesMu.Unlock()
		m.watchMu.Unlock()
		return nil, fail("resource_limit", "watch quota reached")
	}
	if m.watchCallers == nil {
		m.watchCallers = map[string]int{}
	}
	m.watchCount++
	m.watchCallers[w.caller]++
	d.watches[w] = struct{}{}
	d.watchesMu.Unlock()
	m.watchMu.Unlock()
	success := false
	defer func() {
		if !success {
			w.Close()
		}
	}()
	opts := native.WatchOptions{After: after, MaxMemoryBytes: 8 << 20, HeartbeatInterval: 15 * time.Second, Collections: []string{""}}
	for _, n := range names {
		opts.Collections = append(opts.Collections, nativeCollectionName(n))
	}
	var snap *native.Snapshot
	if r.Initial != nil {
		snap, w.cursor, e = b.db.SnapshotAndWatch(ctx, opts)
	} else {
		w.cursor, e = b.db.Watch(ctx, opts)
	}
	if e != nil {
		return nil, watchError(e)
	}
	if snap != nil {
		initialCtx, cancel := context.WithTimeout(ctx, initialTimeout)
		tx := &aptevaTx{ctx: initialCtx, backend: b, snapshot: snap}
		w.Initial, e = execute(tx, r.Initial.Op, r.Initial.Request, d.key)
		snap.Close()
		cancel()
		if e != nil {
			return nil, e
		}
	}
	w.Ready = w.sign(w.cursor.ResumeToken())
	success = true
	context.AfterFunc(ctx, func() { w.Close() })
	return w, nil
}

// Next emits only complete commit boundaries; no native key or document escapes.
// Exactly one goroutine calls Next. Close may run concurrently.
func (w *Watch) Next() (WatchEvent, error) {
	changed := map[string]bool{}
	for w.cursor.Next() {
		c := w.cursor.Change()
		if c.Collection == "" {
			if strings.HasPrefix(string(c.Key), "schema/") {
				name := strings.TrimPrefix(string(c.Key), "schema/")
				if w.selected[name] {
					changed[name] = true
				}
			}
		} else {
			for name := range w.selected {
				if c.Collection == nativeCollectionName(name) {
					changed[name] = true
					break
				}
			}
		}
		if !c.EndOfCommit {
			continue
		}
		names := make([]string, 0, len(changed))
		for n := range changed {
			names = append(names, n)
		}
		sort.Strings(names)
		typ := "progress"
		if len(names) > 0 {
			typ = "invalidate"
		}
		return WatchEvent{typ, names, w.sign(c.CommitToken)}, nil
	}
	return WatchEvent{}, watchError(w.cursor.Err())
}
func (w *Watch) Close() {
	w.once.Do(func() {
		if w.cursor != nil {
			w.cursor.Close()
		}
		w.database.watchesMu.Lock()
		delete(w.database.watches, w)
		w.database.watchesMu.Unlock()
		w.manager.watchMu.Lock()
		w.manager.watchCount--
		w.manager.watchCallers[w.caller]--
		if w.manager.watchCallers[w.caller] == 0 {
			delete(w.manager.watchCallers, w.caller)
		}
		w.manager.watchMu.Unlock()
	})
}
func (d *database) closeWatches() {
	d.watchesMu.Lock()
	ws := make([]*Watch, 0, len(d.watches))
	for w := range d.watches {
		ws = append(ws, w)
	}
	d.watchesMu.Unlock()
	for _, w := range ws {
		w.Close()
	}
}
func (m *Manager) pollChanges(ctx context.Context, scope string, r Request) (any, error) {
	if r.WaitMS < 0 || r.WaitMS > 20000 {
		return nil, Invalid("waitMs must be 0–20000")
	}
	wait := time.Duration(r.WaitMS) * time.Millisecond
	if wait == 0 {
		wait = time.Millisecond
	}
	// The cursor's entire lifetime is bounded; all quotas are released on timeout.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, e := m.OpenWatch(ctx, scope, r)
	if e != nil {
		return nil, e
	}
	defer w.Close()
	timer := time.AfterFunc(wait, cancel)
	defer timer.Stop()
	out := map[string]any{"events": []WatchEvent{}, "cursor": w.Ready}
	if r.Initial != nil {
		out["snapshot"] = w.Initial
	}
	event, e := w.Next()
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	if event.Cursor != "" {
		out["events"] = []WatchEvent{event}
		out["cursor"] = event.Cursor
	}
	return out, nil
}
