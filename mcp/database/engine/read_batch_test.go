package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func readBatchFixture(t *testing.T) (*Manager, func(string, Request) (any, error)) {
	t.Helper()
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	exec := func(op string, r Request) (any, error) { return m.Execute(context.Background(), "p", op, r) }
	must := func(op string, r Request) {
		if _, e := exec(op, r); e != nil {
			t.Fatal(op, e)
		}
	}
	must("database_create", Request{Database: "d", Adapter: "aptevadb"})
	must("collection_create", Request{Database: "d", Collection: "c", Fields: []Field{{"id", "text", false}, {"k", "text", false}, {"n", "integer", false}, {"note", "text", true}}})
	must("index_create", Request{Database: "d", Collection: "c", Index: &Index{Name: "k_n", Fields: []Order{{"k", "asc"}, {"n", "asc"}}}})
	records := []Record{}
	for i := range 1000 {
		records = append(records, Record{"id": fmt.Sprintf("%04d", i), "k": fmt.Sprint("k", i%4), "n": strconv.Itoa(i % 50)})
	}
	must("insert", Request{Database: "d", Collection: "c", Records: records})
	return m, exec
}

// Concurrent reads return what one-at-a-time reads return, in request
// order, for every read operation, including filters shared between
// operations (validation rewrites them in place).
func TestConcurrentReadBatchMatchesSequential(t *testing.T) {
	_, exec := readBatchFixture(t)
	shared := &Filter{And: []Filter{{Field: "k", Op: "in", Value: []any{"k1"}}, {Field: "n", Op: "gte", Value: "10"}}}
	batch := func() []Operation {
		return []Operation{
			{Op: "get", Request: Request{Collection: "c", Key: Record{"id": "0005"}}},
			{Op: "count", Request: Request{Collection: "c", Query: Query{Where: shared}}},
			{Op: "find", Request: Request{Collection: "c", Query: Query{Where: shared, Limit: 5, OrderBy: []Order{{"id", "asc"}}}}},
			{Op: "aggregate", Request: Request{Collection: "c", Query: Query{Where: shared}, GroupBy: []string{"n"}, Metrics: []Metric{{"calls", "count", "", nil}, {"total", "sum", "n", &Filter{Field: "n", Op: "lt", Value: "30"}}}}},
			{Op: "aggregate", Request: Request{Collection: "c", Metrics: []Metric{{"keys", "count_distinct", "k", nil}}}},
			{Op: "count", Request: Request{Collection: "c", Query: Query{Where: shared}}},
		}
	}
	results := map[int]string{}
	for _, n := range []int{1, 2, 4} {
		func() {
			defer func(old int) { readBatchConcurrency = old }(readBatchConcurrency)
			readBatchConcurrency = n
			v, e := exec("batch", Request{Database: "d", Operations: batch()})
			if e != nil {
				t.Fatal(n, e)
			}
			var out map[string]any
			checkErr(t, decode(marshal(v), &out))
			page := out["results"].([]any)[2].(map[string]any)
			// Cursors issued in different seconds have different expiry times
			// and signatures. Check that each authentic cursor resumes the same
			// query at the same row before comparing the batch's stable values.
			resumed, err := exec("find", Request{Database: "d", Collection: "c", Query: Query{Where: shared, Limit: 5, OrderBy: []Order{{"id", "asc"}}, Cursor: page["nextCursor"].(string)}})
			checkErr(t, err)
			var next map[string]any
			checkErr(t, decode(marshal(resumed), &next))
			relEqual(t, "cursor resumes", relList(next["records"])[0]["id"], "0033")
			page["nextCursor"] = "verified resume at 0033"
			results[n] = string(marshal(out))
		}()
	}
	if results[1] != results[2] || results[1] != results[4] {
		t.Fatalf("results differ:\n1: %s\n2: %s\n4: %s", results[1], results[2], results[4])
	}
	if !strings.Contains(results[1], `"count":"200"`) || !strings.Contains(results[1], `"id":"0005"`) {
		t.Fatal("unexpected results", results[1])
	}
}

// The first failing operation in request order fails the batch, whatever
// order the operations finish in, and a response over 4 MiB is refused.
func TestConcurrentReadBatchFailures(t *testing.T) {
	_, exec := readBatchFixture(t)
	count := Operation{Op: "count", Request: Request{Collection: "c"}}
	ops := []Operation{count, count, {Op: "count", Request: Request{Collection: "missing"}}, count, {Op: "find", Request: Request{Collection: "c", Query: Query{Where: &Filter{Field: "nope", Op: "eq", Value: "x"}}}}, count}
	for _, n := range []int{1, 2, 4} {
		func() {
			defer func(old int) { readBatchConcurrency = old }(readBatchConcurrency)
			readBatchConcurrency = n
			_, e := exec("batch", Request{Database: "d", Operations: ops})
			var ee *Error
			if !errors.As(e, &ee) || ee.Code != "not_found" {
				t.Fatalf("concurrency %d: %v", n, e)
			}
		}()
	}
	note := strings.Repeat("x", 4000)
	records := []Record{}
	for i := range 1000 {
		records = append(records, Record{"id": fmt.Sprintf("big%04d", i), "k": "big", "n": "0", "note": note})
	}
	if _, e := exec("insert", Request{Database: "d", Collection: "c", Records: records}); e != nil {
		t.Fatal(e)
	}
	page := Operation{Op: "find", Request: Request{Collection: "c", Query: Query{Where: &Filter{Field: "k", Op: "eq", Value: "big"}, Limit: 500}}}
	big := []Operation{}
	for range 6 {
		big = append(big, page)
	}
	_, e := exec("batch", Request{Database: "d", Operations: big})
	var ee *Error
	if !errors.As(e, &ee) || ee.Code != "resource_limit" {
		t.Fatal("oversized batch", e)
	}
}

// Concurrent reads of one batch share one snapshot: a writer committing
// meanwhile changes none of their results.
func TestConcurrentReadBatchSnapshot(t *testing.T) {
	_, exec := readBatchFixture(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for i := 1000; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, e := exec("insert", Request{Database: "d", Collection: "c", Records: []Record{{"id": strconv.Itoa(i), "k": "k0", "n": "1"}}}); e != nil {
				t.Error(e)
				return
			}
		}
	})
	defer func() { close(stop); wg.Wait() }()
	for range 30 {
		ops := make([]Operation, 12)
		for i := range ops {
			ops[i] = Operation{Op: "count", Request: Request{Collection: "c"}}
		}
		v, e := exec("batch", Request{Database: "d", Operations: ops})
		if e != nil {
			t.Fatal(e)
		}
		out := v.(map[string]any)["results"].([]any)
		for _, r := range out {
			if fmt.Sprint(r) != fmt.Sprint(out[0]) {
				t.Fatalf("results from different snapshots: %v", out)
			}
		}
	}
}
