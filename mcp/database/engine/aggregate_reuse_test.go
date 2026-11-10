package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func reuseFixture(t *testing.T, mode string) *Manager {
	t.Helper()
	opts := DefaultAptevaOptions()
	opts.SyncMode = "os"
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &opts, AggregateReuseMode: mode, MaxConcurrentAptevaQueries: 1})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	for _, project := range []string{"p", "q"} {
		reuseCall(t, m, project, "database_create", Request{Database: "d", Adapter: "aptevadb"})
		reuseCall(t, m, project, "collection_create", Request{Database: "d", Collection: "facts", Fields: []Field{
			{Name: "id", Type: "text"}, {Name: "centre", Type: "text"}, {Name: "day", Type: "text"}, {Name: "caller", Type: "text"},
		}})
		records := []Record{
			{"id": "a", "centre": "A", "day": "2026-10-01", "caller": "x"},
			{"id": "b", "centre": "A", "day": "2026-10-01", "caller": "x"},
			{"id": "c", "centre": "B", "day": "2026-10-01", "caller": "y"},
			{"id": "d", "centre": "A", "day": "2026-09-30", "caller": "z"},
		}
		if project == "q" {
			records = records[:1]
		}
		reuseCall(t, m, project, "insert", Request{Database: "d", Collection: "facts", Records: records})
	}
	return m
}

func reuseCall(t *testing.T, m *Manager, project, op string, r Request) any {
	t.Helper()
	out, err := m.Execute(context.Background(), project, op, r)
	checkErr(t, err)
	return out
}

func reuseQuery(centre string) Request {
	q := Request{Database: "d", Diagnostics: true}
	for _, days := range [][2]string{{"2026-10-01", "2026-11-01"}, {"2026-08-31", "2026-10-01"}} {
		q.Operations = append(q.Operations, Operation{Op: "aggregate", Request: Request{
			Collection: "facts", Query: Query{Where: &Filter{And: []Filter{
				{Field: "centre", Op: "eq", Value: centre},
				{Field: "day", Op: "gte", Value: days[0]},
				{Field: "day", Op: "lt", Value: days[1]},
			}}},
			Metrics: []Metric{{Name: "calls", Op: "count"}, {Name: "callers", Op: "count_distinct", Field: "caller"}},
		}})
	}
	return q
}

func waitForReuse(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(until) {
			t.Fatal("timed out waiting for aggregate reuse state")
		}
		time.Sleep(time.Millisecond)
	}
}

func reuseCounts(m *Manager) (int, int) {
	s := m.aggregateReads
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := 0
	for _, flight := range s.flights {
		waiters += flight.waiters
	}
	return len(s.flights), waiters
}

type reuseReply struct {
	value any
	err   error
}

func asyncReuse(m *Manager, ctx context.Context, q Request) <-chan reuseReply {
	done := make(chan reuseReply, 1)
	go func() {
		result, err := m.Execute(ctx, "p", "batch", q)
		done <- reuseReply{result, err}
	}()
	return done
}

func reuseRows(v any) []any { return v.(map[string]any)["results"].([]any) }

func reuseRow(v any, i int) Record {
	return reuseRows(v)[i].(map[string]any)["rows"].([]Record)[0]
}

func reusePath(v any) any { return v.(map[string]any)["diagnostics"].(map[string]any)["reuse"] }

func TestAggregateReuseModes(t *testing.T) {
	var expected []any
	for _, mode := range []string{"off", "coalesce", "cache"} {
		t.Run(mode, func(t *testing.T) {
			m := reuseFixture(t, mode)
			unblock, err := m.acquireNativeQuery(context.Background())
			checkErr(t, err)
			var once sync.Once
			t.Cleanup(func() { once.Do(unblock) })
			var replies []<-chan reuseReply
			for range 8 {
				replies = append(replies, asyncReuse(m, context.Background(), reuseQuery("A")))
			}
			waitForReuse(t, func() bool {
				if mode == "off" {
					return len(m.nativeQueryAdmissions) == 9
				}
				flights, waiters := reuseCounts(m)
				return flights == 1 && waiters == 8 && len(m.nativeQueryAdmissions) == 2
			})
			once.Do(unblock)
			executions, joined := 0, 0
			for _, done := range replies {
				reply := <-done
				checkErr(t, reply.err)
				if expected == nil {
					expected = reuseRows(reply.value)
				}
				if !reflect.DeepEqual(reuseRows(reply.value), expected) {
					t.Fatalf("batch mismatch: %v", reply.value)
				}
				diag := reply.value.(map[string]any)["diagnostics"].(map[string]any)
				if diag["executed"] == true {
					executions++
				}
				if diag["reuse"] == "inflight" {
					joined++
					if _, ok := diag["executionMs"]; ok {
						t.Fatal("waiter inherited execution timing")
					}
				}
			}
			if mode == "off" && executions != 8 || mode != "off" && (executions != 1 || joined != 7) {
				t.Fatalf("executions=%d joins=%d", executions, joined)
			}
			for range 8 {
				out := reuseCall(t, m, "p", "batch", reuseQuery("A"))
				if !reflect.DeepEqual(reuseRows(out), expected) {
					t.Fatal(out)
				}
				if mode == "cache" && reusePath(out) != "cache" || mode == "coalesce" && reusePath(out) != "executed" {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestAggregateReuseRevisionRaceAndCancellation(t *testing.T) {
	m := reuseFixture(t, "cache")
	unblock, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	var once sync.Once
	t.Cleanup(func() { once.Do(unblock) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := asyncReuse(m, ctx, reuseQuery("A"))
	waitForReuse(t, func() bool { return len(m.nativeQueryAdmissions) == 2 })
	follower := asyncReuse(m, context.Background(), reuseQuery("A"))
	waitForReuse(t, func() bool { _, n := reuseCounts(m); return n == 2 })
	cancel()
	if reply := <-leader; !errors.Is(reply.err, context.Canceled) {
		t.Fatal(reply)
	}

	// The older flight is still queued. This commit creates a new key,
	// and the next caller must register its own flight before execution.
	reuseCall(t, m, "p", "insert", Request{Database: "d", Collection: "facts", Records: []Record{
		{"id": "e", "centre": "A", "day": "2026-10-01", "caller": "new"},
	}})
	afterCommit := asyncReuse(m, context.Background(), reuseQuery("A"))
	waitForReuse(t, func() bool { f, n := reuseCounts(m); return f == 2 && n == 2 })
	once.Do(unblock)
	for _, channel := range []<-chan reuseReply{follower, afterCommit} {
		reply := <-channel
		checkErr(t, reply.err)
		if row := reuseRow(reply.value, 0); row["calls"] != "3" || row["callers"] != "2" {
			t.Fatal(row)
		}
	}
	s := m.aggregateReads
	s.mu.Lock()
	entries := len(s.entries)
	s.mu.Unlock()
	if entries != 1 {
		t.Fatalf("published %d entries; expected only the revision read", entries)
	}
	warm := reuseCall(t, m, "p", "batch", reuseQuery("A"))
	if reusePath(warm) != "cache" || reuseRow(warm, 0)["calls"] != "3" {
		t.Fatal(warm)
	}
	reuseCall(t, m, "p", "delete", Request{Database: "d", Collection: "facts", Key: Record{"id": "e"}})
	fresh := reuseCall(t, m, "p", "batch", reuseQuery("A"))
	if reusePath(fresh) != "executed" || reuseRow(fresh, 0)["calls"] != "2" {
		t.Fatal(fresh)
	}
}

func TestAggregateReuseIsolationAndFailure(t *testing.T) {
	m := reuseFixture(t, "cache")
	first := reuseCall(t, m, "p", "batch", reuseQuery("A"))
	reuseRow(first, 0)["calls"] = "modified caller response"
	for _, tt := range []struct{ project, centre, calls, path string }{
		{"p", "A", "2", "cache"}, {"p", "B", "1", "executed"}, {"q", "A", "1", "executed"},
	} {
		out := reuseCall(t, m, tt.project, "batch", reuseQuery(tt.centre))
		if reuseRow(out, 0)["calls"] != tt.calls || reusePath(out) != tt.path {
			t.Fatal(tt, out)
		}
	}
	plain := reuseQuery("A")
	plain.Diagnostics, plain.TimeoutMS = false, 2000
	plainResult := reuseCall(t, m, "p", "batch", plain).(map[string]any)
	if _, exists := plainResult["diagnostics"]; exists || reuseRow(plainResult, 0)["calls"] != "2" {
		t.Fatal(plainResult)
	}
	state := m.aggregateReads
	state.mu.Lock()
	for _, item := range state.entries {
		item.Value.(*aggregateEntry).expires = time.Now().Add(-time.Second)
	}
	state.mu.Unlock()
	expired := reuseCall(t, m, "p", "batch", reuseQuery("A"))
	if reusePath(expired) != "executed" {
		t.Fatal(expired)
	}
	reversed := reuseQuery("A")
	reversed.Operations[0], reversed.Operations[1] = reversed.Operations[1], reversed.Operations[0]
	out := reuseCall(t, m, "p", "batch", reversed)
	if reusePath(out) != "executed" || reuseRow(out, 0)["calls"] != "1" {
		t.Fatal(out)
	}
	missing := reuseQuery("A")
	missing.Operations[0].Request.Collection = "missing"
	for range 2 {
		if _, err := m.Execute(context.Background(), "p", "batch", missing); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatal(err)
		}
		if flights, _ := reuseCounts(m); flights != 0 {
			t.Fatal("failed flight remained registered")
		}
	}
	bad := reuseQuery("A")
	bad.Operations[0].Request.Where.Or = []Filter{}
	if reusableAggregateBatch(bad) != nil {
		t.Fatal("canonicalization must not erase an empty invalid branch")
	}
}

func TestAggregateResultCacheBounds(t *testing.T) {
	s := newAggregateReuse(true)
	defer s.close()
	big := map[string]any{"results": []any{strings.Repeat("x", aggregateEntryBytes)}}
	s.storeEntry(aggregateKey{}, big)
	if len(s.entries) != 0 {
		t.Fatal("oversize entry retained")
	}
	value := map[string]any{"results": []any{strings.Repeat("x", aggregateEntryBytes/2)}}
	for i := range 200 {
		s.storeEntry(aggregateKey{revision: uint64(i)}, value)
	}
	if s.bytes > aggregateCacheBytes || s.entries[aggregateKey{}] != nil ||
		s.entries[aggregateKey{revision: 199}] == nil {
		t.Fatalf("cache eviction failed, retained %d bytes", s.bytes)
	}
}

func TestAggregateReuseCancelsAbandonedFlight(t *testing.T) {
	m := reuseFixture(t, "cache")
	unblock, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	defer unblock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := asyncReuse(m, ctx, reuseQuery("A"))
	waitForReuse(t, func() bool { return len(m.nativeQueryAdmissions) == 2 })
	cancel()
	if reply := <-done; !errors.Is(reply.err, context.Canceled) {
		t.Fatal(reply)
	}
	waitForReuse(t, func() bool {
		s := m.aggregateReads
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active == 0 && len(s.flights) == 0 && len(s.entries) == 0 && len(m.nativeQueryAdmissions) == 1
	})
}
