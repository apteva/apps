package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// changeFeedPlatform scripts db_changes: an open (no cursor) returns the
// current boundary, and a resume blocks until a commit or cancellation.
type changeFeedPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu      sync.Mutex
	opens   int
	active  int
	seq     int
	inputs  []map[string]any
	openErr error
	failOn  map[string]error
	commits chan string
}

func (p *changeFeedPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.CallAppResultContext(context.Background(), app, tool, input, out)
}

func (p *changeFeedPlatform) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if app != "database" || tool != "db_changes" {
		return fmt.Errorf("unexpected %s.%s", app, tool)
	}
	p.mu.Lock()
	p.inputs = append(p.inputs, input)
	after, _ := input["after"].(string)
	var result map[string]any
	if after == "" {
		p.opens++
		if p.openErr != nil {
			err := p.openErr
			p.mu.Unlock()
			return err
		}
		result = map[string]any{"events": []any{}, "cursor": fmt.Sprintf("c%d", p.seq)}
		p.mu.Unlock()
	} else {
		if err := p.failOn[after]; err != nil {
			delete(p.failOn, after)
			p.mu.Unlock()
			return err
		}
		p.active++
		p.mu.Unlock()
		defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
		select {
		case collection := <-p.commits:
			p.mu.Lock()
			p.seq++
			cursor := fmt.Sprintf("c%d", p.seq)
			p.mu.Unlock()
			result = map[string]any{"events": []any{map[string]any{"type": "invalidate", "collections": []any{collection}, "cursor": cursor}}, "cursor": cursor}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

func (p *changeFeedPlatform) snapshot() (opens, active int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens, p.active
}

func changeFeedApp(t *testing.T) (*App, *changeFeedPlatform) {
	t.Helper()
	p := &changeFeedPlatform{commits: make(chan string), failOn: map[string]error{}}
	a := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p)), hub: newSubscriptionHub()}
	t.Cleanup(func() { a.stopDatabaseWatches(); a.hub.close() })
	return a, p
}

var ordersWatch = databaseWatchSpec{project: "p1", database: "prod", scope: "project", collections: []string{"orders"}}

func expectInvalidation(t *testing.T, sub *subscription, collections ...string) {
	t.Helper()
	select {
	case payload := <-sub.events:
		if fmt.Sprint(payload["collections"]) != fmt.Sprint(collections) || payload["database"] != "prod" {
			t.Fatalf("invalidation payload: %v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no invalidation delivered")
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDatabaseWatchIsSharedAndStopsWithLastSubscriber(t *testing.T) {
	a, p := changeFeedApp(t)
	sub, cancel := a.hub.subscribeCoalescedForAPI("p1", "default", ordersWatch.topic())
	defer cancel()
	first, err := a.acquireDatabaseWatch(context.Background(), ordersWatch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.acquireDatabaseWatch(context.Background(), ordersWatch)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "watcher never resumed from its boundary", func() bool { _, active := p.snapshot(); return active == 1 })
	if opens, _ := p.snapshot(); opens != 1 {
		t.Fatalf("subscribers did not share one watch: %d opens", opens)
	}
	p.mu.Lock()
	open, resume := p.inputs[0], p.inputs[1]
	p.mu.Unlock()
	if open["waitMs"] != 0 || open["scope"] != "project" || open["database"] != "prod" || !reflect.DeepEqual(open["collections"], []string{"orders"}) {
		t.Fatalf("open input: %v", open)
	}
	if resume["after"] != "c0" || resume["waitMs"] != databaseWatchWaitMS {
		t.Fatalf("resume input: %v", resume)
	}

	p.commits <- "orders"
	expectInvalidation(t, sub, "orders")
	first()
	p.commits <- "orders"
	expectInvalidation(t, sub, "orders")
	second()
	eventually(t, "watcher kept polling after the last subscriber left", func() bool {
		_, active := p.snapshot()
		a.dbWatchMu.Lock()
		defer a.dbWatchMu.Unlock()
		return active == 0 && len(a.dbWatches) == 0
	})
}

func TestDatabaseWatchOpenFailureIsReportedAndRetried(t *testing.T) {
	a, p := changeFeedApp(t)
	p.openErr = errors.New("database.db_changes: unsupported: realtime currently requires aptevadb")
	if _, err := a.acquireDatabaseWatch(context.Background(), ordersWatch); err == nil || err.Error() != p.openErr.Error() {
		t.Fatalf("open failure not reported: %v", err)
	}
	a.dbWatchMu.Lock()
	left := len(a.dbWatches)
	a.dbWatchMu.Unlock()
	if left != 0 {
		t.Fatal("failed watcher was kept")
	}
	p.mu.Lock()
	p.openErr = nil
	p.mu.Unlock()
	release, err := a.acquireDatabaseWatch(context.Background(), ordersWatch)
	if err != nil {
		t.Fatalf("watch did not retry after a failed open: %v", err)
	}
	release()
}

func TestDatabaseWatchResyncsAfterLostHistory(t *testing.T) {
	a, p := changeFeedApp(t)
	p.failOn["c0"] = errors.New("database.db_changes: history_expired: load a fresh snapshot to resume")
	sub, cancel := a.hub.subscribeCoalescedForAPI("p1", "default", ordersWatch.topic())
	defer cancel()
	release, err := a.acquireDatabaseWatch(context.Background(), ordersWatch)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Lost history may hide commits, so every subscriber re-runs once the
	// watch has a new boundary.
	expectInvalidation(t, sub, "orders")
	if opens, _ := p.snapshot(); opens != 2 {
		t.Fatalf("watch did not reopen: %d opens", opens)
	}
}

func TestCoalescedSubscriptionsDropRedundantInvalidations(t *testing.T) {
	hub := newSubscriptionHub()
	defer hub.close()
	sub, cancel := hub.subscribeCoalescedForAPI("p1", "default", "database:project:prod:orders")
	defer cancel()
	for range 100 {
		hub.publishAllAPIs("p1", "database:project:prod:orders", map[string]any{})
	}
	select {
	case <-sub.slow:
		t.Fatal("burst of invalidations failed the subscriber")
	default:
	}
	if len(sub.events) != 1 {
		t.Fatalf("pending invalidations: %d", len(sub.events))
	}
}

func TestDatabaseWatchForResolver(t *testing.T) {
	source := sourceRecord{Kind: "database", Config: map[string]any{"database": "prod", "collection": "orders"}}
	spec, ok := databaseWatchFor("p1", source, resolverRecord{Operation: "aggregate"})
	if !ok || spec.database != "prod" || spec.scope != "project" || fmt.Sprint(spec.collections) != "[orders]" {
		t.Fatalf("default watch: %#v %v", spec, ok)
	}
	spec, ok = databaseWatchFor("p1", source, resolverRecord{Operation: "count", Config: map[string]any{"watch_collections": []any{"refunds", "orders", "refunds"}}})
	if !ok || fmt.Sprint(spec.collections) != "[orders refunds]" || spec.topic() != "database:project:prod:orders,refunds" {
		t.Fatalf("watch_collections: %#v", spec)
	}
	if _, ok := databaseWatchFor("p1", source, resolverRecord{Operation: "insert"}); ok {
		t.Fatal("write resolver produced a watch")
	}
	private := sourceRecord{Kind: "database", Config: map[string]any{"collection": "orders", "scope": "app"}}
	if spec, _ := databaseWatchFor("p1", private, resolverRecord{Operation: "find"}); spec.scope != "app" || spec.database != "default" {
		t.Fatalf("source scope: %#v", spec)
	}
}

func TestSubscriptionReRunNeverJoinsAnOlderExecution(t *testing.T) {
	var runtime executionRuntime
	defer runtime.stop()
	limits := defaultReleaseLimits()
	release := make(chan struct{})
	var runs sync.WaitGroup
	result := func(value string) func(context.Context) (executeResult, error) {
		return func(context.Context) (executeResult, error) {
			<-release
			return executeResult{Data: map[string]any{"v": value}}, nil
		}
	}
	shared := func(ctx context.Context, value string) <-chan any {
		out := make(chan any, 1)
		runs.Add(1)
		go func() {
			defer runs.Done()
			got, _ := runtime.share(ctx, "same-query", limits, result(value))
			out <- got.Data["v"]
		}()
		return out
	}
	stale := shared(context.Background(), "before-commit")
	time.Sleep(20 * time.Millisecond) // the first flight is running
	joiner := shared(context.Background(), "unused")
	time.Sleep(20 * time.Millisecond) // the joiner is waiting on the first flight
	invalidated := context.WithValue(context.Background(), sharedAfterKey{}, time.Now())
	fresh := shared(invalidated, "after-commit")
	time.Sleep(20 * time.Millisecond)
	close(release)
	runs.Wait()
	if got := <-stale; got != "before-commit" {
		t.Fatalf("first caller: %v", got)
	}
	if got := <-joiner; got != "before-commit" {
		t.Fatalf("ordinary caller stopped sharing: %v", got)
	}
	if got := <-fresh; got != "after-commit" {
		t.Fatalf("re-run adopted a result read before its invalidation: %v", got)
	}
}
