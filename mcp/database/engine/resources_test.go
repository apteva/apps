package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func resourceManager(t *testing.T, slots int) *Manager {
	t.Helper()
	o := DefaultAptevaOptions()
	o.CacheBytes = 1 << 20
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o, AptevaCacheBudgetBytes: int64(slots) << 20, MaxConcurrentAptevaQueries: 1})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	return m
}
func resourceDatabase(t *testing.T, m *Manager, name string) {
	t.Helper()
	call(t, m, "p", "database_create", Request{Database: name, Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: name, Collection: "items", Fields: []Field{{Name: "value", Type: "integer"}}})
	call(t, m, "p", "insert", Request{Database: name, Collection: "items", Records: []Record{{"id": "one", "value": "7"}}})
}
func TestNativeCacheEvictionPreservesDataAndLeases(t *testing.T) {
	m := resourceManager(t, 2)
	resourceDatabase(t, m, "a")
	resourceDatabase(t, m, "b")
	a, releaseA, err := m.acquireDatabase(context.Background(), "p", "a", "", false)
	checkErr(t, err)
	b, err := m.resolve(context.Background(), "p", "b", "", false)
	checkErr(t, err)
	resourceDatabase(t, m, "c")
	if a.closed || !b.closed {
		t.Fatal("active database evicted or idle database retained")
	}
	c, releaseC, err := m.acquireDatabase(context.Background(), "p", "c", "", false)
	checkErr(t, err)
	_, err = m.Execute(context.Background(), "p", "database_create", Request{Database: "blocked", Adapter: "aptevadb"})
	expectCode(t, err, "resource_limit")
	if a.closed || c.closed {
		t.Fatal("leased database evicted")
	}
	releaseA()
	releaseC()
	result := call(t, m, "p", "get", Request{Database: "b", Collection: "items", Key: Record{"id": "one"}}).(map[string]any)
	if !result["found"].(bool) || result["record"].(Record)["value"] != "7" {
		t.Fatal(result)
	}
	dbs := call(t, m, "p", "databases_list", Request{}).([]DatabaseInfo)
	if len(dbs) != 3 {
		t.Fatal("eviction changed registry", dbs)
	}
}
func TestNativeWatchesPinCacheAndInitialRespectsQueryQueue(t *testing.T) {
	m := resourceManager(t, 1)
	resourceDatabase(t, m, "a")
	enabled := true
	call(t, m, "p", "realtime_configure", Request{Database: "a", Enabled: &enabled})
	done, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	_, err = m.OpenWatch(context.Background(), "p", Request{Database: "a", Collections: []string{"items"}, Initial: &Operation{Op: "count", Request: Request{Collection: "items", TimeoutMS: 5}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if m.watchCount != 0 {
		t.Fatal("queued initial snapshot registered a watch")
	}
	done()
	w, err := m.OpenWatch(context.Background(), "p", Request{Database: "a", Collections: []string{"items"}, Initial: &Operation{Op: "count", Request: Request{Collection: "items"}}})
	checkErr(t, err)
	defer w.Close()
	if w.Initial.(map[string]any)["count"] != "1" {
		t.Fatal(w.Initial)
	}
	// Setup's query deadline must not be the lifetime of the live watch.
	call(t, m, "p", "insert", Request{Database: "a", Collection: "items", Records: []Record{{"id": "two", "value": "8"}}})
	event, err := w.Next()
	checkErr(t, err)
	if event.Type != "invalidate" {
		t.Fatal(event)
	}
	_, err = m.Execute(context.Background(), "p", "database_create", Request{Database: "b", Adapter: "aptevadb"})
	expectCode(t, err, "resource_limit")
	w.Close()
	resourceDatabase(t, m, "b")
	if call(t, m, "p", "count", Request{Database: "a", Collection: "items"}).(map[string]any)["count"] != "2" {
		t.Fatal("watch eviction lost data")
	}
}
func TestNativeQueryAdmissionDoesNotPinSnapshotOrBlockPoints(t *testing.T) {
	m := resourceManager(t, 2)
	resourceDatabase(t, m, "a")
	resourceDatabase(t, m, "b")
	done, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = m.Execute(ctx, "p", "count", Request{Database: "b", Collection: "items"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	d, err := m.resolve(context.Background(), "p", "b", "", false)
	checkErr(t, err)
	if d.backend.(*aptevaBackend).db.Stats().Snapshots != 0 {
		t.Fatal("queued request pinned a snapshot")
	}
	if !call(t, m, "p", "get", Request{Database: "a", Collection: "items", Key: Record{"id": "one"}}).(map[string]any)["found"].(bool) {
		t.Fatal("point read")
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err = m.acquireNativeQuery(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeQueryQueueIsBoundedAndCancellationReleasesLeases(t *testing.T) {
	m := resourceManager(t, 1)
	resourceDatabase(t, m, "a")
	done, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	errs := make(chan error, maxQueuedNativeQueries)
	for i := 0; i < maxQueuedNativeQueries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Execute(ctx, "p", "count", Request{Database: "a", Collection: "items"})
			errs <- err
		}()
	}
	until := time.Now().Add(2 * time.Second)
	for len(m.nativeQueryAdmissions) < cap(m.nativeQueryAdmissions) && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(m.nativeQueryAdmissions) != cap(m.nativeQueryAdmissions) {
		cancel()
		done()
		wg.Wait()
		t.Fatal("waiters did not enter queue")
	}
	_, err = m.Execute(context.Background(), "p", "count", Request{Database: "a", Collection: "items"})
	expectCode(t, err, "resource_limit")
	cancel()
	wg.Wait()
	for i := 0; i < maxQueuedNativeQueries; i++ {
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	done()
	if len(m.nativeQueryAdmissions) != 0 || len(m.nativeQueries) != 0 {
		t.Fatal("query slots leaked")
	}
	call(t, m, "p", "database_create", Request{Database: "b", Adapter: "aptevadb"})
}
func TestNativeCacheConcurrentChurn(t *testing.T) {
	m := resourceManager(t, 2)
	for _, name := range []string{"a", "b", "c"} {
		resourceDatabase(t, m, name)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				r := Request{Database: []string{"a", "b", "c"}[(i+worker)%3], Collection: "items", Key: Record{"id": "one"}}
				for retry := 0; ; retry++ {
					v, err := m.Execute(context.Background(), "p", "get", r)
					if err == nil {
						if !v.(map[string]any)["found"].(bool) {
							errs <- fmt.Errorf("lost row in %s", r.Database)
						}
						break
					}
					var app *Error
					if !errors.As(err, &app) || app.Code != "resource_limit" || retry >= 100 {
						errs <- err
						return
					}
					time.Sleep(time.Millisecond)
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
func TestNativeResourceOptionsValidate(t *testing.T) {
	for _, o := range []ManagerOptions{{AptevaCacheBudgetBytes: 1}, {MaxConcurrentAptevaQueries: -1}, {MaxConcurrentAptevaQueries: 65}, {Apteva: &AptevaOptions{CacheBytes: 64 << 20}, AptevaCacheBudgetBytes: 32 << 20}} {
		if m, err := OpenWithConfig(t.TempDir(), o); err == nil {
			m.Close()
			t.Fatal("invalid resource options accepted", o)
		}
	}
}
