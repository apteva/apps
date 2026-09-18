package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func pebbleFixture(t *testing.T) (*Manager, *database, *pebbleBackend) {
	t.Helper()
	m := manager(t)
	fixture(t, m, "pebble")
	d, err := m.resolve(context.Background(), "p", "shop", "", false)
	if err != nil {
		t.Fatal(err)
	}
	return m, d, d.backend.(*pebbleBackend)
}

func TestGroupedRequestRollbackAndVisibility(t *testing.T) {
	m, d, b := pebbleFixture(t)
	call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "by_email", Fields: []Order{{Field: "email"}}, Unique: true}})
	request := func(op string, r Request) transactionCall {
		r.Collection = "orders"
		return transactionCall{context.Background(), func(tx transaction) error { _, err := execute(tx, op, r, d.key); return err }}
	}
	insert := func(id, email string) Record { return Record{"id": id, "email": email, "active": true} }
	var commits int
	calls := []transactionCall{
		request("insert", Request{Records: []Record{insert("new", "new@example.test")}}),
		// The first record of this request is valid; the second conflicts with
		// an earlier group member. Neither record of this request may survive.
		request("insert", Request{Records: []Record{insert("rollback", "rollback@example.test"), insert("conflict", "new@example.test")}}),
		request("update", Request{Key: Record{"id": "new"}, Set: Record{"amount": 7.}}),
		request("insert", Request{Records: []Record{insert("reused", "rollback@example.test")}}),
	}
	d.mu.Lock()
	errs := b.combine(calls, func(batch *pebble.Batch) error { commits++; return b.commit(batch) })
	d.mu.Unlock()
	if commits != 1 {
		t.Fatalf("commits=%d, want 1", commits)
	}
	for _, i := range []int{0, 2, 3} {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
	}
	expectCode(t, errs[1], "unique_conflict")
	for _, id := range []string{"rollback", "conflict"} {
		v := call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": id}}).(map[string]any)
		if v["found"] != false {
			t.Fatalf("failed request leaked %s", id)
		}
	}
	v := call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "new"}}).(map[string]any)["record"].(Record)
	if number(v["amount"]) != 7. {
		t.Fatalf("later request did not see earlier member: %v", v)
	}
	root := m.root
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	v = call(t, reopened, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "new"}}).(map[string]any)["record"].(Record)
	if number(v["amount"]) != 7. {
		t.Fatal("acknowledged data missing after reopen")
	}
}

func TestGroupedCancellationAndCommitErrors(t *testing.T) {
	_, d, b := pebbleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	boom := errors.New("simulated sync failure")
	calls := []transactionCall{
		{ctx, func(transaction) error { called = true; return nil }},
		{context.Background(), func(tx transaction) error {
			_, e := execute(tx, "insert", Request{Collection: "orders", Records: []Record{{"id": "uncommitted", "active": true}}}, d.key)
			return e
		}},
		{context.Background(), func(transaction) error { return Invalid("bad request") }},
	}
	d.mu.Lock()
	errs := b.combine(calls, func(*pebble.Batch) error { return boom })
	d.mu.Unlock()
	if called || !errors.Is(errs[0], context.Canceled) || !errors.Is(errs[1], boom) {
		t.Fatalf("unexpected outcomes %v", errs)
	}
	expectCode(t, errs[2], "invalid_argument")
	// Cancellation after callback work discards that request before staging.
	ctx, cancel = context.WithCancel(context.Background())
	errs = b.combine([]transactionCall{{ctx, func(tx transaction) error {
		_, e := execute(tx, "insert", Request{Collection: "orders", Records: []Record{{"id": "cancelled", "active": true}}}, d.key)
		cancel()
		return e
	}}}, func(*pebble.Batch) error { t.Fatal("cancelled request reached commit"); return nil })
	if !errors.Is(errs[0], context.Canceled) {
		t.Fatal(errs)
	}
}

type gatedCommitBackend struct {
	*pebbleBackend
	entered chan struct{}
	release chan struct{}
}

func (b *gatedCommitBackend) transactions(calls []transactionCall) []error {
	return b.combine(calls, func(batch *pebble.Batch) error { close(b.entered); <-b.release; return b.commit(batch) })
}

func TestGroupedCommitIsBarrierForResponsesReadsAndClose(t *testing.T) {
	for _, action := range []string{"read", "close", "drop"} {
		t.Run(action, func(t *testing.T) {
			m, d, b := pebbleFixture(t)
			gate := &gatedCommitBackend{b, make(chan struct{}), make(chan struct{})}
			d.backend = gate
			writeDone := make(chan error, 1)
			go func() {
				_, e := m.Execute(context.Background(), "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": "gated", "active": true}}})
				writeDone <- e
			}()
			<-gate.entered
			otherDone := make(chan error, 1)
			go func() {
				if action == "close" {
					otherDone <- m.Close()
					return
				}
				op := "get"
				r := Request{Database: "shop", Collection: "orders", Key: Record{"id": "gated"}}
				if action == "drop" {
					op = "database_drop"
					r.Confirm = true
				}
				v, e := m.Execute(context.Background(), "p", op, r)
				if e == nil && action == "read" && v.(map[string]any)["found"] != true {
					e = errors.New("read missed durable write")
				}
				otherDone <- e
			}()
			select {
			case e := <-writeDone:
				t.Fatalf("write returned before commit: %v", e)
			case e := <-otherDone:
				t.Fatalf("%s returned before commit: %v", action, e)
			case <-time.After(20 * time.Millisecond):
			}
			close(gate.release)
			if e := <-writeDone; e != nil {
				t.Fatal(e)
			}
			if e := <-otherDone; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestGroupedConcurrentIncrements(t *testing.T) {
	m, _, _ := pebbleFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				_, e := m.Execute(context.Background(), "p", "update", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}, Increment: Record{"amount": 1.}})
				if e != nil {
					errs <- e
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	v := call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}}).(map[string]any)["record"].(Record)
	if number(v["amount"]) != 138. {
		t.Fatalf("lost update: %v", v)
	}
}

func TestWriteQueueBound(t *testing.T) {
	d := &database{}
	d.writes.running = true
	d.writes.pending = make([]*queuedWrite, maxQueuedWrites)
	_, err := d.enqueueWrite(context.Background(), nil, func(transaction) (any, error) { return nil, fmt.Errorf("must not execute") })
	expectCode(t, err, "resource_limit")
}

func TestGroupSizeLimitDoesNotSplitOneTransaction(t *testing.T) {
	_, _, b := pebbleFixture(t)
	large := bytes.Repeat([]byte{'x'}, 600<<10)
	var commits int
	largeFinished := false
	calls := []transactionCall{
		{context.Background(), func(tx transaction) error {
			batch := tx.(*pebbleTx).batch
			if err := batch.Set([]byte("probe/one"), large, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte("probe/two"), large, nil); err != nil {
				return err
			}
			largeFinished = true
			return nil
		}},
		{context.Background(), func(tx transaction) error { return tx.(*pebbleTx).batch.Set([]byte("probe/three"), []byte("ok"), nil) }},
	}
	errs := b.combine(calls, func(batch *pebble.Batch) error {
		if !largeFinished {
			t.Fatal("split an individual transaction")
		}
		commits++
		return b.commit(batch)
	})
	if commits != 2 {
		t.Fatalf("large group not split between requests: %d commits", commits)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"probe/one", "probe/two", "probe/three"} {
		_, closer, err := b.db.Get([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		closer.Close()
	}
}
