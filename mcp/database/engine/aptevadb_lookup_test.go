package engine

import (
	"context"
	"fmt"
	"testing"
)

// Count physical document fetches inside the real related-join scan, before
// the caller sees only the rows which survived its scope predicate.
type lookupWorkTx struct {
	transaction
	documents int64
	hints     []string
}

func (tx *lookupWorkTx) scanRecords(c Collection, q Query, fields []string, visit func(Record) bool) error {
	if c.Name == "calls" {
		tx.hints = append(tx.hints, q.hint)
		q.examined = &tx.documents
	}
	return tx.transaction.(recordScanner).scanRecords(c, q, fields, visit)
}

func TestAptevaSelectiveScopedLookups(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	// Put centre before the aliases, as real schemas can do. Index creation
	// order must not turn an identifier lookup into a centre-wide scan.
	relCollection(rt, "calls", "id", "id text, local_id text, raw_call_id text, centre text, label text", "centre", "local_id", "raw_call_id")
	relCollection(rt, "reports", "id", "id text, centre text")
	relCollection(rt, "links", "id", "id text, parent text, ref text", "parent")
	seed := func(start, end int) {
		for i := start; i < end; i += 500 {
			rows := []map[string]any{}
			for j := i; j < i+500 && j < end; j++ {
				id := fmt.Sprintf("c%05d", j)
				rows = append(rows, map[string]any{"id": id, "local_id": id, "raw_call_id": id, "centre": "a", "label": "match"})
			}
			relInsert(rt, "calls", rows...)
		}
	}
	seed(0, 10000)
	relInsert(rt, "calls", map[string]any{"id": "dup-a", "local_id": "alias", "raw_call_id": "alias", "centre": "a", "label": "match"}, map[string]any{"id": "dup-b", "local_id": "alias", "raw_call_id": "alias", "centre": "a", "label": "match"}, map[string]any{"id": "foreign", "local_id": "alias", "raw_call_id": "c00001", "centre": "b", "label": "match"})
	for i, ref := range []string{"c00001", "c00002", "missing", "alias"} {
		id := fmt.Sprint(i)
		relInsert(rt, "reports", map[string]any{"id": id, "centre": "a"})
		relInsert(rt, "links", map[string]any{"id": id, "parent": id, "ref": ref})
	}
	d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
	checkErr(t, e)
	defer release()
	run := func() (int64, int64) {
		var joinWork, selectionWork int64
		checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error {
			c, e := tx.collection("calls")
			if e != nil {
				return e
			}
			for _, tc := range []struct {
				field, value, centre string
				want                 int
			}{
				{"id", "c00001", "a", 1}, {"id", "c00001", "b", 0}, {"id", "missing", "a", 0},
				{"local_id", "c00001", "a", 1}, {"raw_call_id", "c00001", "a", 1},
				{"local_id", "alias", "a", 2}, {"raw_call_id", "alias", "a", 2},
			} {
				where := &Filter{And: []Filter{{Field: tc.field, Op: "eq", Value: tc.value}, {Field: "centre", Op: "eq", Value: tc.centre}}}
				before := string(marshal(where))
				q := Query{Where: where, Select: []string{"id"}, diag: &countDiagnostics{}}
				rows, e := tx.find(c, q, 100)
				if e != nil {
					return e
				}
				if len(rows) != tc.want {
					t.Fatalf("find %s/%s/%s: %v", tc.field, tc.value, tc.centre, rows)
				}
				q.diag = &countDiagnostics{}
				counts, e := tx.aggregate(c, Request{Query: q, Metrics: []Metric{{Name: "n", Op: "count"}}}, Collection{})
				if e != nil {
					return e
				}
				relEqual(t, "count", counts[0]["n"], fmt.Sprint(tc.want))
				if q.diag.stats == nil || q.diag.stats.DocumentFetches > 3 {
					t.Fatalf("unbounded count %s: %+v", tc.field, q.diag.stats)
				}
				if q.diag.selectionEntries > 32 {
					t.Fatalf("unbounded planning: %+v", q.diag)
				}
				selectionWork += q.diag.selectionEntries
				relEqual(t, "predicate unchanged", string(marshal(where)), before)
				// Streaming deliberately ignores limit and order, but keeps a
				// supplied internal hint and all scope predicates.
				q.hint = "primary"
				if tc.field != "id" {
					q.hint = "by_" + tc.field
				}
				q.OrderBy = []Order{{Field: "centre", Direction: "desc"}}
				q.Limit = 1
				var docs int64
				q.examined = &docs
				n := 0
				e = tx.(recordScanner).scanRecords(c, q, []string{"id"}, func(row Record) bool { n++; return true })
				if e != nil {
					return e
				}
				if n != tc.want || docs > 3 {
					t.Fatalf("stream lost hint or scope: n=%d docs=%d want=%d", n, docs, tc.want)
				}
			}
			var r Request
			checkErr(t, decode([]byte(`{"collection":"reports","as":"p","where":{"field":"centre","op":"eq","value":"a"},"relation":{"as":"context","limit":40,"sources":[{"name":"calls","joins":[{"collection":"links","as":"l","on":[{"local":"p.id","foreign":"parent"}]},{"collection":"calls","as":"c","where":{"field":"centre","op":"eq","value":"a"},"anyOf":[{"on":[{"local":"l.ref","foreign":"id"}]},{"on":[{"local":"l.ref","foreign":"local_id"}]},{"on":[{"local":"l.ref","foreign":"raw_call_id"}]}]}],"distinct":["c.id"],"select":["c.id","c.label"]}]},"match":{"field":"c.label","op":"contains_ascii","value":"match"}}`), &r))
			before := string(marshal(r))
			audit := &lookupWorkTx{transaction: tx}
			page := r
			page.Count = true
			page.Select = []string{"id"}
			found, e := execute(audit, "find", page, d.key)
			if e != nil {
				return e
			}
			out := map[string]any{}
			checkErr(t, decode(marshal(found), &out))
			relEqual(t, "find total", out["total"], "3")
			for _, row := range relList(out["records"]) {
				want := 1
				if row["id"] == "3" {
					want = 2
				}
				relEqual(t, "physical dedup", len(relList(row["context"])), want)
			}
			r.Metrics = []Metric{{Name: "n", Op: "count"}}
			summary, e := execute(audit, "aggregate", r, d.key)
			if e != nil {
				return e
			}
			out = map[string]any{}
			checkErr(t, decode(marshal(summary), &out))
			relEqual(t, "related aggregate parity", relList(out["rows"])[0]["n"], "3")
			r.Metrics = nil
			relEqual(t, "request unchanged", string(marshal(r)), before)
			if audit.documents > 40 || audit.documents == 0 {
				t.Fatalf("actual join documents=%d hints=%v", audit.documents, audit.hints)
			}
			for _, hint := range audit.hints {
				if hint == "" || hint == "by_centre" {
					t.Fatalf("join hint=%q", hint)
				}
			}
			joinWork = audit.documents
			return nil
		}))
		t.Logf("calls lookup documents=%d planning entries=%d", joinWork, selectionWork)
		return joinWork, selectionWork
	}
	docs, probes := run()
	seed(10000, 20000)
	laterDocs, laterProbes := run()
	relEqual(t, "join work independent of centre size", laterDocs, docs)
	relEqual(t, "planning work independent of centre size", laterProbes, probes)
}

func TestPlanExactNonNullKeys(t *testing.T) {
	c := Collection{PrimaryKey: []string{"id"}, Fields: []Field{{Name: "id", Type: "text"}, {Name: "centre", Type: "text"}, {Name: "alias", Type: "text", Nullable: true}, {Name: "other", Type: "text", Nullable: true}}, Indexes: []Index{
		{Name: "scope", Fields: []Order{{Field: "centre"}}},
		{Name: "unique_alias", Unique: true, Fields: []Order{{Field: "alias"}}},
		{Name: "unique_pair", Unique: true, Fields: []Order{{Field: "alias"}, {Field: "other"}}},
	}}
	for _, tc := range []struct {
		name       string
		where      []Filter
		hint, want string
	}{
		{"primary", []Filter{{Field: "id", Op: "eq", Value: "1"}, {Field: "centre", Op: "eq", Value: "a"}}, "", "primary"},
		{"unique", []Filter{{Field: "alias", Op: "eq", Value: "1"}, {Field: "centre", Op: "eq", Value: "a"}}, "", "unique_alias"},
		{"composite unique", []Filter{{Field: "alias", Op: "eq", Value: "1"}, {Field: "other", Op: "eq", Value: "2"}, {Field: "centre", Op: "eq", Value: "a"}}, "", "unique_pair"},
		{"explicit", []Filter{{Field: "id", Op: "eq", Value: "1"}, {Field: "centre", Op: "eq", Value: "a"}}, "scope", "scope"},
		{"partial composite", []Filter{{Field: "other", Op: "eq", Value: "x"}, {Field: "centre", Op: "eq", Value: "a"}}, "", "scope"},
		{"nullable unique", []Filter{{Field: "alias", Op: "is_null"}, {Field: "centre", Op: "eq", Value: "a"}}, "", "scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := plan(c, Query{Where: &Filter{And: tc.where}, hint: tc.hint})
			relEqual(t, "index", p.Index, tc.want)
		})
	}
}

func TestAptevaScopedLookupSnapshotAndOverlay(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	relCollection(rt, "calls", "id", "id text, alias text, centre text", "centre", "alias")
	relInsert(rt, "calls", map[string]any{"id": "one", "alias": "same", "centre": "a"}, map[string]any{"id": "foreign", "alias": "same", "centre": "b"})
	d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
	checkErr(t, e)
	defer release()
	q := Query{Where: &Filter{And: []Filter{{Field: "alias", Op: "eq", Value: "same"}, {Field: "centre", Op: "eq", Value: "a"}}}, hint: "by_alias", Select: []string{"id"}}
	check := func(tx transaction, want int) {
		t.Helper()
		c, e := tx.collection("calls")
		checkErr(t, e)
		n := 0
		checkErr(t, tx.(recordScanner).scanRecords(c, q, q.Select, func(r Record) bool { n++; return true }))
		relEqual(t, "scoped streaming rows", n, want)
		r, e := tx.aggregate(c, Request{Query: q, Metrics: []Metric{{Name: "n", Op: "count"}}}, Collection{})
		checkErr(t, e)
		relEqual(t, "scoped count", r[0]["n"], fmt.Sprint(want))
	}
	checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error {
		check(tx, 1)
		relInsert(rt, "calls", map[string]any{"id": "later", "alias": "same", "centre": "a"})
		check(tx, 1)
		return nil
	}))
	checkErr(t, d.backend.transaction(context.Background(), true, func(tx transaction) error {
		check(tx, 2)
		_, e := execute(tx, "delete", Request{Collection: "calls", Key: Record{"id": "one"}}, d.key)
		if e != nil {
			return e
		}
		check(tx, 1)
		_, e = execute(tx, "insert", Request{Collection: "calls", Records: []Record{{"id": "staged", "alias": "same", "centre": "a"}, {"id": "staged-foreign", "alias": "same", "centre": "b"}}}, d.key)
		if e != nil {
			return e
		}
		check(tx, 2)
		return nil
	}))
}
