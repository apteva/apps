package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func realtimeFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	m := manager(t)
	m.apteva.SyncMode = "os"
	fixture(t, m, "aptevadb")
	enabled := true
	call(t, m, "p", "realtime_configure", Request{Database: "shop", Enabled: &enabled})
	return m, "p"
}
func openTestWatch(t *testing.T, m *Manager, scope string, r Request) *Watch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	w, e := m.OpenWatch(ctx, scope, r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(w.Close)
	return w
}
func nextTestWatch(t *testing.T, w *Watch) WatchEvent {
	t.Helper()
	e, err := w.Next()
	if err != nil || e.Cursor == "" {
		t.Fatalf("watch: %+v %v", e, err)
	}
	return e
}
func TestRealtimeSnapshotCommitReplayAndRollback(t *testing.T) {
	m, s := realtimeFixture(t)
	req := Request{Database: "shop", Collections: []string{"orders"}, Initial: &Operation{Op: "find", Request: Request{Collection: "orders", Query: Query{Limit: 50}}}}
	w := openTestWatch(t, m, s, req)
	if len(rows(t, w.Initial)) != 4 {
		t.Fatal(w.Initial)
	}
	boundary := w.Ready
	call(t, m, s, "batch", Request{Database: "shop", Operations: []Operation{
		{Op: "update", Request: Request{Collection: "orders", Key: Record{"id": "a"}, Set: Record{"amount": 99.}}},
		{Op: "delete", Request: Request{Collection: "orders", Key: Record{"id": "b"}}},
	}})
	event := nextTestWatch(t, w)
	if event.Type != "invalidate" || len(event.Collections) != 1 || event.Collections[0] != "orders" || event.Cursor == boundary {
		t.Fatal(event)
	}
	// Resuming the earlier delivered boundary replays this whole commit.
	replay := openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"orders"}, After: boundary})
	if got := nextTestWatch(t, replay); got.Cursor != event.Cursor {
		t.Fatal(got, event)
	}
	w.Close()
	replay.Close()
	_, e := m.Execute(context.Background(), s, "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": "a", "active": true}, {"id": "new", "active": false}}})
	expectCode(t, e, "unique_conflict")
	// A failed write adds no change; the next successful commit has one invalidation.
	w = openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"orders"}, After: event.Cursor})
	call(t, m, s, "delete", Request{Database: "shop", Collection: "orders", Key: Record{"id": "c"}})
	if got := nextTestWatch(t, w); got.Type != "invalidate" {
		t.Fatal(got)
	}
}
func TestRealtimeSnapshotBoundaryUnderWrites(t *testing.T) {
	m, s := realtimeFixture(t)
	call(t, m, s, "collection_create", Request{Database: "shop", Collection: "counter", Fields: []Field{{Name: "n", Type: "integer"}}})
	call(t, m, s, "insert", Request{Database: "shop", Collection: "counter", Records: []Record{{"id": "n", "n": "0"}}})
	for i := 0; i < 25; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := m.Execute(context.Background(), s, "update", Request{Database: "shop", Collection: "counter", Key: Record{"id": "n"}, Set: Record{"n": fmt.Sprint(n)}})
			if e != nil {
				t.Error(e)
			}
		}(i + 1)
		w := openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"counter"}, Initial: &Operation{Op: "find", Request: Request{Collection: "counter"}}})
		seen := rows(t, w.Initial)[0]["n"].(string)
		wg.Wait()
		if seen != fmt.Sprint(i+1) {
			if e := nextTestWatch(t, w); e.Type != "invalidate" {
				t.Fatal(e)
			}
		}
		w.Close()
	}
}
func TestRealtimeScopeAndCatalogIsolation(t *testing.T) {
	m, s := realtimeFixture(t)
	w := openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"orders"}})
	// Add another collection, then selected schema changes; no private catalog data is returned.
	call(t, m, s, "collection_create", Request{Database: "shop", Collection: "secret", Fields: []Field{{Name: "x", Type: "text"}}})
	call(t, m, s, "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "by_country", Fields: []Order{{Field: "country"}}}})
	got := nextTestWatch(t, w)
	for got.Type == "progress" {
		got = nextTestWatch(t, w)
	}
	if got.Type != "invalidate" || len(got.Collections) != 1 || got.Collections[0] != "orders" {
		t.Fatal(got)
	}
	_, e := m.OpenWatch(context.Background(), s, Request{Database: "shop", Collections: []string{"secret"}, After: got.Cursor})
	expectCode(t, e, "resume_invalid")
	call(t, m, "other", "database_create", Request{Database: "shop", Adapter: "aptevadb"})
	call(t, m, "other", "realtime_configure", Request{Database: "shop", Enabled: func() *bool { x := true; return &x }()})
	_, e = m.OpenWatch(context.Background(), "other", Request{Database: "shop", Collections: []string{"orders"}, After: got.Cursor})
	expectCode(t, e, "resume_invalid")
}
func TestRealtimeRestartDisableAndQuotas(t *testing.T) {
	root := t.TempDir()
	m, e := Open(root)
	checkErr(t, e)
	m.apteva.SyncMode = "os"
	defer func() { m.Close() }()
	fixture(t, m, "aptevadb")
	yes := true
	call(t, m, "p", "realtime_configure", Request{Database: "shop", Enabled: &yes})
	w := openTestWatch(t, m, "p", Request{Database: "shop", Collections: []string{"orders"}})
	cursor := w.Ready
	w.Close()
	checkErr(t, m.Close())
	m, e = Open(root)
	checkErr(t, e)
	w = openTestWatch(t, m, "p", Request{Database: "shop", Collections: []string{"orders"}, After: cursor})
	call(t, m, "p", "delete", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}})
	nextTestWatch(t, w)
	w.Close()
	watches := []*Watch{}
	for i := 0; i < 8; i++ {
		watches = append(watches, openTestWatch(t, m, "p", Request{Database: "shop", Collections: []string{"orders"}}))
	}
	_, e = m.OpenWatch(context.Background(), "p", Request{Database: "shop", Collections: []string{"orders"}})
	expectCode(t, e, "resource_limit")
	// Idle watchers leave ordinary point reads/writes available.
	call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "b"}})
	for _, w := range watches {
		w.Close()
	}
	no := false
	call(t, m, "p", "realtime_configure", Request{Database: "shop", Enabled: &no})
	call(t, m, "p", "realtime_configure", Request{Database: "shop", Enabled: &yes})
	_, e = m.OpenWatch(context.Background(), "p", Request{Database: "shop", Collections: []string{"orders"}, After: cursor})
	expectCode(t, e, "resume_invalid")
}
func TestRealtimeExpiryCancellationAndUnsupported(t *testing.T) {
	m, s := realtimeFixture(t)
	yes := true
	call(t, m, s, "realtime_configure", Request{Database: "shop", Enabled: &yes, RetentionSeconds: 1})
	w := openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"orders"}})
	token := w.Ready
	w.Close()
	call(t, m, s, "delete", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}})
	time.Sleep(1100 * time.Millisecond)
	_, e := m.OpenWatch(context.Background(), s, Request{Database: "shop", Collections: []string{"orders"}, After: token})
	expectCode(t, e, "history_expired")
	ctx, cancel := context.WithCancel(context.Background())
	w, e = m.OpenWatch(ctx, s, Request{Database: "shop", Collections: []string{"orders"}})
	checkErr(t, e)
	cancel()
	_, e = w.Next()
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	w.Close()
	m.watchMu.Lock()
	count := m.watchCount
	m.watchMu.Unlock()
	if count != 0 {
		t.Fatal(count)
	}
	call(t, m, s, "database_create", Request{Database: "sql", Adapter: "sqlite"})
	_, e = m.OpenWatch(context.Background(), s, Request{Database: "sql", Collections: []string{"orders"}})
	expectCode(t, e, "unsupported")
}

func TestRealtimeProcessQuotaAndDrop(t *testing.T) {
	m, s := realtimeFixture(t)
	yes := true
	for _, name := range []string{"two", "three"} {
		call(t, m, s, "database_create", Request{Database: name, Adapter: "aptevadb"})
		call(t, m, s, "realtime_configure", Request{Database: name, Enabled: &yes})
	}
	ws := []*Watch{}
	for _, name := range []string{"shop", "two"} {
		for i := 0; i < 32; i++ {
			ws = append(ws, openTestWatch(t, m, s, Request{Database: name, Collections: []string{"orders"}, WatchCaller: fmt.Sprint(i)}))
		}
	}
	_, e := m.OpenWatch(context.Background(), s, Request{Database: "three", Collections: []string{"orders"}})
	expectCode(t, e, "resource_limit")
	call(t, m, s, "database_drop", Request{Database: "two", Confirm: true})
	w := openTestWatch(t, m, s, Request{Database: "three", Collections: []string{"orders"}})
	w.Close()
	for _, w := range ws {
		w.Close()
	}
}

func TestRealtimeDropRecreateAndCancellation(t *testing.T) {
	m, s := realtimeFixture(t)
	w := openTestWatch(t, m, s, Request{Database: "shop", Collections: []string{"orders"}})
	call(t, m, s, "collection_drop", Request{Database: "shop", Collection: "orders", Confirm: true})
	got := nextTestWatch(t, w)
	for got.Type == "progress" {
		got = nextTestWatch(t, w)
	}
	if got.Type != "invalidate" {
		t.Fatal(got)
	}
	call(t, m, s, "collection_create", Request{Database: "shop", Collection: "orders", Fields: []Field{{Name: "n", Type: "integer"}}})
	got = nextTestWatch(t, w)
	for got.Type == "progress" {
		got = nextTestWatch(t, w)
	}
	if got.Type != "invalidate" {
		t.Fatal(got)
	}
	w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	w, e := m.OpenWatch(ctx, s, Request{Database: "shop", Collections: []string{"orders"}})
	checkErr(t, e)
	done := make(chan error, 1)
	go func() { _, e := w.Next(); done <- e }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle watch did not cancel")
	}
	w.Close()
}
