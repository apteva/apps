package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// seedVisits writes rows whose id rises while started_at falls, so the compound index
// returns one centre's rows in the reverse of primary key order.
func seedVisits(t *testing.T, rt relT) {
	t.Helper()
	relCollection(rt, "visits", "id", "id integer,centre text,started_at text,customer text,score integer,pad text")
	relCall(rt, "index_create", `{"collection":"visits","index":{"name":"by_centre_at","fields":[{"field":"centre","direction":"asc"},{"field":"started_at","direction":"asc"}]}}`)
	relCollection(rt, "customers", "id", "id text,name text")
	relCollection(rt, "tags", "id", "id text,visit_id integer,tag text", "visit_id")
	visits, tags := []map[string]any{}, []map[string]any{}
	for i := 1; i <= visitCount; i++ {
		visits = append(visits, visitRow(i))
		for n := 0; n < i%4; n++ {
			tags = append(tags, map[string]any{"id": fmt.Sprintf("%d-%d", i, n), "visit_id": strconv.Itoa(i), "tag": fmt.Sprintf("tag-%d", (i+n)%3)})
		}
	}
	relInsert(rt, "visits", visits...)
	relInsert(rt, "tags", tags...)
	customers := []map[string]any{}
	// Some customers have no record, so inner and left joins differ.
	for i := 0; i < 14; i++ {
		customers = append(customers, map[string]any{"id": fmt.Sprintf("cust-%02d", i), "name": fmt.Sprintf("name-%02d", (i*5)%14)})
	}
	relInsert(rt, "customers", customers...)
}

const visitCount = 300

func visitRow(i int) map[string]any {
	return map[string]any{
		"id": strconv.Itoa(i), "centre": fmt.Sprintf("c%d", i%3), "started_at": fmt.Sprintf("2026-10-01T%05d", visitCount-i),
		"customer": fmt.Sprintf("cust-%02d", i%17), "score": strconv.Itoa(i % 5), "pad": fmt.Sprintf("pad-%d", i),
	}
}

const (
	visitsOneCentre = `{"and":[{"field":"centre","op":"eq","value":"c1"},{"field":"started_at","op":"gte","value":"2026-10-01T00000"}]}`
	visitsTwo       = `{"and":[{"field":"centre","op":"in","value":["c0","c2"]},{"field":"started_at","op":"gte","value":"2026-10-01T00050"}]}`
)

// visitShapes cross what depends on arrival order, with what could: groups, pick,
// distinct, joins and correlated filters.
var visitShapes = map[string]string{
	"flat":      `"orderBy":[{"field":"root.score","direction":"desc"}],"select":["root.id","root.score"]`,
	"max":       `"group":{"by":["root.customer"],"orderBy":[{"field":"root.started_at","direction":"desc","agg":"max"},{"field":"root.customer","direction":"asc"}]},"orderBy":[{"field":"root.score"}],"select":["root.id","root.score"]`,
	"min":       `"group":{"by":["root.customer"],"orderBy":[{"field":"root.score","direction":"asc","agg":"min"},{"field":"root.customer","direction":"desc"}]},"orderBy":[{"field":"root.started_at","direction":"desc"}],"select":["root.id"]`,
	"pick":      `"pick":{"by":["root.customer"],"orderBy":[{"field":"root.score","direction":"desc"}]},"orderBy":[{"field":"root.customer"}],"select":["root.id","root.customer"]`,
	"distinct":  `"distinct":["root.customer"],"orderBy":[{"field":"root.customer"}],"select":["root.id","root.customer"]`,
	"left":      `"joins":[{"as":"who","collection":"customers","kind":"left","on":[{"local":"root.customer","foreign":"id"}]}],"orderBy":[{"field":"who.name","direction":"desc"}],"select":["root.id","who.name"]`,
	"inner":     `"joins":[{"as":"who","collection":"customers","on":[{"local":"root.customer","foreign":"id"}]}],"group":{"by":["who.name"],"orderBy":[{"field":"root.score","direction":"desc","agg":"max"},{"field":"who.name","direction":"asc"}]},"orderBy":[{"field":"root.id"}],"select":["root.id","who.name"]`,
	"fanout":    `"joins":[{"as":"tag","collection":"tags","on":[{"local":"root.id","foreign":"visit_id"}]}],"group":{"by":["root.customer"],"orderBy":[{"field":"tag.tag","direction":"asc","agg":"min"},{"field":"root.customer","direction":"asc"}]},"orderBy":[{"field":"tag.tag","direction":"desc"}],"select":["root.id","tag.tag"]`,
	"fandist":   `"joins":[{"as":"tag","collection":"tags","on":[{"local":"root.id","foreign":"visit_id"}]}],"distinct":["tag.tag","root.customer"],"orderBy":[{"field":"root.customer"}],"select":["root.id","tag.id"]`,
	"fanpick":   `"joins":[{"as":"tag","collection":"tags","on":[{"local":"root.id","foreign":"visit_id"}]}],"pick":{"by":["root.customer"],"orderBy":[{"field":"tag.tag","direction":"desc"}]},"orderBy":[{"field":"root.customer"}],"select":["root.id","tag.id"]`,
	"semi":      `"joins":[{"as":"has","collection":"tags","kind":"semi","on":[{"local":"root.id","foreign":"visit_id"}]}],"orderBy":[{"field":"root.score"}],"select":["root.id"]`,
	"anti":      `"joins":[{"as":"has","collection":"tags","kind":"anti","on":[{"local":"root.id","foreign":"visit_id"}]}],"orderBy":[{"field":"root.score"}],"select":["root.id"]`,
	"aggregate": `"joins":[{"as":"n","collection":"tags","kind":"aggregate","on":[{"local":"root.id","foreign":"visit_id"}],"metrics":[{"name":"count","op":"count"}]}],"orderBy":[{"field":"n.count","direction":"desc"}],"select":["root.id","n.count"]`,
}

// visitWheres read through an index that can't return primary key order, through a
// union of its ranges, through correlated filters, and through no index at all.
var visitWheres = map[string]string{
	"indexed":    visitsOneCentre,
	"union":      visitsTwo,
	"exists":     `{"and":[{"field":"centre","op":"eq","value":"c1"},{"exists":{"collection":"tags","on":[{"localField":"id","foreignField":"visit_id"}]}}]}`,
	"not_exists": `{"and":[{"field":"centre","op":"eq","value":"c1"},{"not_exists":{"collection":"tags","on":[{"localField":"id","foreignField":"visit_id"}]}}]}`,
	"scan":       `{"field":"score","op":"gte","value":"1"}`,
}

var visitContradictions = map[string]bool{
	"exists/anti": true, "not_exists/semi": true,
	"not_exists/fanout": true, "not_exists/fandist": true, "not_exists/fanpick": true,
}

// answerOf is what a client relies on. A cursor is sealed differently on every run.
func answerOf(out map[string]any) string {
	return string(marshal(map[string]any{"total": out["total"], "hasMore": out["hasMore"], "groups": out["groups"], "records": out["records"]}))
}

// One read in any order must answer what batches in primary key order answer: SQLite
// and Pebble still read in batches, so they are the reference for AptevaDB.
func TestJoinedPageIgnoresArrivalOrder(t *testing.T) {
	answers := map[string]map[string]string{}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		rt := relOpen(t, adapter)
		seedVisits(t, rt)
		answers[adapter] = map[string]string{}
		for whereName, where := range visitWheres {
			for shapeName, shape := range visitShapes {
				name := whereName + "/" + shapeName
				whole := relCall(rt, "joined_page", `{"collection":"visits","where":%s,%s,"limit":1000}`, where, shape)
				total, _ := strconv.Atoi(fmt.Sprint(whole["total"]))
				answers[adapter][name] = answerOf(whole)
				if total == 0 {
					// Both sides of these ask opposite things of the same rows.
					if !visitContradictions[name] {
						t.Fatalf("%s %s: nothing matched, so it proves nothing", adapter, name)
					}
					continue
				}
				// The same read by offset and by cursor, seven items at a time.
				byOffset, byCursor := []any{}, []any{}
				key := "records"
				if strings.Contains(shape, `"group"`) {
					key = "groups"
				}
				cursor := ""
				for offset := 0; offset < total; offset += 7 {
					page := relCall(rt, "joined_page", `{"collection":"visits","where":%s,%s,"limit":7,"offset":%d}`, where, shape, offset)
					byOffset = append(byOffset, page[key].([]any)...)
					extra := ""
					if cursor != "" {
						extra = fmt.Sprintf(`,"cursor":%q`, cursor)
					}
					next := relCall(rt, "joined_page", `{"collection":"visits","where":%s,%s,"limit":7%s}`, where, shape, extra)
					byCursor = append(byCursor, next[key].([]any)...)
					cursor, _ = next["nextCursor"].(string)
				}
				relEqual(t, adapter+" "+name+" by offset", string(marshal(byOffset)), string(marshal(whole[key])))
				relEqual(t, adapter+" "+name+" by cursor", string(marshal(byCursor)), string(marshal(whole[key])))
			}
		}
		if adapter == "aptevadb" {
			// Otherwise this test proves nothing about arrival order.
			if ids := rootArrivals(t, rt, visitsOneCentre); sort.IntsAreSorted(ids) {
				t.Fatalf("rows arrived in primary key order: %v", ids[:5])
			}
		}
	}
	for name, want := range answers["sqlite"] {
		relEqual(t, "pebble "+name, answers["pebble"][name], want)
		relEqual(t, "aptevadb "+name, answers["aptevadb"][name], want)
	}
}

// rootArrivals lists the visits a one-read scan produces, in the order it produces them.
func rootArrivals(t *testing.T, rt relT, where string) []int {
	t.Helper()
	ctx := context.Background()
	d, release, e := rt.m.acquireDatabase(ctx, "p", "d", "", false)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	var r Request
	if e := decode([]byte(`{"where":`+where+`}`), &r); e != nil {
		t.Fatal(e)
	}
	ids := []int{}
	e = d.backend.transaction(ctx, false, func(tx transaction) error {
		tx = d.cachedTransaction(tx, true)
		c, e := tx.collection("visits")
		if e != nil {
			return e
		}
		return tx.(recordScanner).scanRecords(c, Query{Where: r.Where}, []string{"id"}, func(row Record) bool {
			n, _ := strconv.Atoi(fmt.Sprint(row["id"]))
			ids = append(ids, n)
			return true
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	return ids
}

// Two answers checked against the rows themselves, not only between adapters.
func TestJoinedPageWinnersDoNotDependOnArrival(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedVisits(t, rt)
		firstId, bestScore := map[string]int{}, map[string]int{}
		bestId := map[string]int{}
		for i := 1; i <= visitCount; i++ {
			row := visitRow(i)
			if row["centre"] != "c1" {
				continue
			}
			customer := row["customer"].(string)
			if _, seen := firstId[customer]; !seen {
				firstId[customer] = i
			}
			score, _ := strconv.Atoi(row["score"].(string))
			// The tie is the row's physical key, compared as encoded bytes.
			if old, seen := bestScore[customer]; !seen || score > old {
				bestScore[customer], bestId[customer] = score, i
			}
		}
		out := relCall(rt, "joined_page", `{"collection":"visits","where":%s,%s,"limit":100}`, visitsOneCentre, visitShapes["distinct"])
		records := relList(out["records"])
		relEqual(t, "distinct total", len(records), len(firstId))
		for _, row := range records {
			// Distinct keeps the first row in primary key order, whichever way the index reads.
			relEqual(t, "distinct winner of "+fmt.Sprint(row["root.customer"]), fmt.Sprint(row["root.id"]), strconv.Itoa(firstId[row["root.customer"].(string)]))
		}
		out = relCall(rt, "joined_page", `{"collection":"visits","where":%s,%s,"limit":100}`, visitsOneCentre, visitShapes["pick"])
		records = relList(out["records"])
		relEqual(t, "pick total", len(records), len(bestId))
		for _, row := range records {
			id, _ := strconv.Atoi(fmt.Sprint(row["root.id"]))
			customer := row["root.customer"].(string)
			score, _ := strconv.Atoi(visitRow(id)["score"].(string))
			relEqual(t, "pick winner's score of "+customer, score, bestScore[customer])
		}
	})
}

// A group's min and max compare each field by its own type: as text, "10" sorts before "9".
func TestJoinedPageGroupMinMaxUsesEachFieldsType(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "scores", "id", "id integer,kind text,who text,score integer")
		rows := []map[string]any{}
		for _, player := range []struct {
			who    string
			scores []int
		}{{"a", []int{9, 2}}, {"b", []int{10, 9}}, {"c", []int{100, 20}}, {"d", []int{100, 5}}} {
			for _, score := range player.scores {
				rows = append(rows, map[string]any{"id": strconv.Itoa(len(rows) + 1), "kind": "same", "who": player.who, "score": strconv.Itoa(score)})
			}
		}
		relInsert(rt, "scores", rows...)
		for _, tc := range []struct {
			agg, direction, want string
		}{
			{"max", "desc", "c d b a"},
			{"min", "asc", "a d b c"},
		} {
			out := relCall(rt, "joined_page", `{"collection":"scores","group":{"by":["root.who"],"orderBy":[{"field":"root.kind","direction":"asc","agg":"min"},{"field":"root.score","direction":"%s","agg":"%s"},{"field":"root.who","direction":"asc"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":10}`, tc.direction, tc.agg)
			got := []string{}
			for _, group := range relList(out["groups"]) {
				got = append(got, fmt.Sprint(group["key"].(map[string]any)["root.who"]))
			}
			relEqual(t, tc.agg+" order", strings.Join(got, " "), tc.want)
		}
		// A group's own rows and pick's winners sort by more than one field in the same way.
		const byScore = `[{"field":"root.kind"},{"field":"root.score","direction":"desc"}]`
		legs := relCall(rt, "joined_page", `{"collection":"scores","group":{"by":["root.who"],"orderBy":[{"field":"root.who","direction":"asc"}]},"orderBy":%s,"select":["root.score"],"limit":10}`, byScore)
		got := []string{}
		for _, group := range relList(legs["groups"]) {
			for _, row := range relList(group["rows"]) {
				got = append(got, fmt.Sprint(row["root.score"]))
			}
		}
		relEqual(t, "rows within groups", strings.Join(got, " "), "9 2 10 9 100 20 100 5")
		picked := relCall(rt, "joined_page", `{"collection":"scores","pick":{"by":["root.who"],"orderBy":%s},"orderBy":[{"field":"root.who"}],"select":["root.score"],"limit":10}`, byScore)
		got = []string{}
		for _, row := range relList(picked["records"]) {
			got = append(got, fmt.Sprint(row["root.score"]))
		}
		relEqual(t, "pick winners", strings.Join(got, " "), "9 10 100 100")
	})
}

// rootReadTx counts the reads of one collection, passing on what the wrapper offers.
type rootReadTx struct {
	transaction
	root         string
	finds, scans int
	eachRow      func()
}

func (t *rootReadTx) find(c Collection, q Query, limit int) ([]Record, error) {
	if c.Name == t.root {
		t.finds++
	}
	return t.transaction.find(c, q, limit)
}
func (t *rootReadTx) unorderedRoots() bool {
	r, ok := t.transaction.(unorderedRootReader)
	return ok && r.unorderedRoots()
}
func (t *rootReadTx) scanRecords(c Collection, q Query, fields []string, consume func(Record) bool) error {
	if c.Name != t.root {
		return t.transaction.(recordScanner).scanRecords(c, q, fields, consume)
	}
	t.scans++
	return t.transaction.(recordScanner).scanRecords(c, q, fields, func(r Record) bool {
		if t.eachRow != nil {
			t.eachRow()
		}
		return consume(r)
	})
}

func runVisitPage(t *testing.T, ctx context.Context, rt relT, body string, wrap func(*rootReadTx)) (*rootReadTx, map[string]any, error) {
	t.Helper()
	d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	var r Request
	if e := decode([]byte(body), &r); e != nil {
		t.Fatal(e)
	}
	r.Database = "d"
	count := &rootReadTx{root: "visits"}
	if wrap != nil {
		wrap(count)
	}
	var result any
	e = d.backend.transaction(ctx, false, func(tx transaction) error {
		count.transaction = d.cachedTransaction(tx, true)
		c, e := count.collection("visits")
		if e != nil {
			return e
		}
		result, e = executeJoinedPage(count, c, r, []byte("k"))
		return e
	})
	out := map[string]any{}
	if e == nil {
		decode(marshal(result), &out)
	}
	return count, out, e
}

// A one-centre page used to read every row of the centre once per batch of 1,000.
func TestJoinedPageReadsRootsOnce(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	seedVisits(t, rt)
	body := func(shape string) string {
		return fmt.Sprintf(`{"collection":"visits","where":%s,%s,"limit":5}`, visitsOneCentre, shape)
	}
	// The case that re-reads: its index can't return primary key order.
	var r Request
	if e := decode([]byte(body(visitShapes["max"])), &r); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	d, release, e := rt.m.acquireDatabase(ctx, "p", "d", "", false)
	if e != nil {
		t.Fatal(e)
	}
	e = d.backend.transaction(ctx, false, func(tx transaction) error {
		c, e := tx.collection("visits")
		if e != nil {
			return e
		}
		if !plan(c, Query{Where: r.Where, OrderBy: []Order{{"id", "asc"}}}).Sort {
			t.Fatal("this index can return primary key order, so it proves nothing")
		}
		return nil
	})
	release()
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"flat", "max", "min", "pick", "left", "inner", "fanout", "fanpick", "semi", "anti", "aggregate"} {
		count, out, e := runVisitPage(t, ctx, rt, body(visitShapes[name]), nil)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		if count.finds != 0 || count.scans != 1 {
			t.Fatalf("%s: %d batches and %d scans of the roots, want one scan", name, count.finds, count.scans)
		}
		if out["total"] == "0" {
			t.Fatalf("%s: nothing matched", name)
		}
	}
	// Distinct keeps the first row it sees, so it still reads in primary key order.
	for _, name := range []string{"distinct", "fandist"} {
		count, _, e := runVisitPage(t, ctx, rt, body(visitShapes[name]), nil)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		if count.finds == 0 || count.scans != 0 {
			t.Fatalf("%s: %d batches and %d scans of the roots, want batches only", name, count.finds, count.scans)
		}
	}
	for _, adapter := range []string{"sqlite", "pebble"} {
		rt := relOpen(t, adapter)
		seedVisits(t, rt)
		count, _, e := runVisitPage(t, ctx, rt, body(visitShapes["max"]), nil)
		if e != nil {
			t.Fatalf("%s: %v", adapter, e)
		}
		if count.finds == 0 || count.scans != 0 {
			t.Fatalf("%s: %d batches and %d scans of the roots, want batches only", adapter, count.finds, count.scans)
		}
	}
}

// Through the Manager, where the transaction is wrapped: rows that can't fit a batch
// only fit if the single read really runs there.
func TestJoinedPageNeedsNoRootBatchMemory(t *testing.T) {
	options := DefaultAptevaOptions()
	options.QueryWorkBytes = 1 << 20
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &options})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	rt := relT{t, m}
	relCall(rt, "database_create", `{"adapter":"aptevadb"}`)
	relCollection(rt, "wide", "id", "id integer,pad text")
	const rows = 2000
	pad := strings.Repeat("x", 1500)
	for start := 0; start < rows; start += MaxRecords {
		batch := []map[string]any{}
		for i := start; i < start+MaxRecords && i < rows; i++ {
			batch = append(batch, map[string]any{"id": strconv.Itoa(i), "pad": pad})
		}
		relInsert(rt, "wide", batch...)
	}
	page := `{"collection":"wide",%s"orderBy":[{"field":"root.pad"},{"field":"root.id","direction":"desc"}],"select":["root.id"],"limit":3}`
	out := relCall(rt, "joined_page", page, "")
	relEqual(t, "total", out["total"], strconv.Itoa(rows))
	relEqual(t, "first", relStrings(relList(out["records"]), "root.id")[0], strconv.Itoa(rows-1))
	// With distinct the batches are still there, so this limit is too.
	_, e = relTry(rt, "joined_page", page, `"distinct":["root.id"],`)
	var failure *Error
	if !errors.As(e, &failure) || failure.Code != "resource_limit" {
		t.Fatalf("distinct read its batches: %v", e)
	}
}

// rootScanStub reuses one record, as the adapter's decoder does.
type rootScanStub struct {
	transaction
	rows   []Record
	before func(int)
	err    error
	reads  int
	finds  int
	offers bool
}

func (s *rootScanStub) unorderedRoots() bool { return s.offers }
func (s *rootScanStub) scanRecords(_ Collection, _ Query, _ []string, consume func(Record) bool) error {
	scratch := Record{}
	for i, row := range s.rows {
		if s.before != nil {
			s.before(i)
		}
		clear(scratch)
		for k, v := range row {
			scratch[k] = v
		}
		scratch["filter_only"] = "x"
		s.reads++
		if !consume(scratch) {
			return nil
		}
	}
	return s.err
}
func (s *rootScanStub) find(Collection, Query, int) ([]Record, error) {
	s.finds++
	return nil, nil
}

func TestEachRootOnce(t *testing.T) {
	c := Collection{Name: "r", PrimaryKey: []string{"id"}}
	order := []Order{{"id", "asc"}}
	rows := []Record{{"id": "a"}, {"id": "b"}, {"id": "c"}}
	ctx := context.Background()
	stub := func() *rootScanStub { return &rootScanStub{rows: rows, offers: true} }
	collect := func(out *[]Record) func(Record) (bool, error) {
		return func(r Record) (bool, error) {
			*out = append(*out, r)
			return true, nil
		}
	}
	t.Run("keeps its own copy of each row", func(t *testing.T) {
		var got []Record
		budget := &relBudget{ctx: ctx}
		if e := eachRootOnce(stub(), c, nil, nil, order, []string{"id"}, budget, collect(&got)); e != nil {
			t.Fatal(e)
		}
		relEqual(t, "rows", string(marshal(got)), `[{"id":"a"},{"id":"b"},{"id":"c"}]`)
		relEqual(t, "work", budget.work, int64(3))
	})
	t.Run("stops at a failure that reads as a match", func(t *testing.T) {
		state := &existsState{}
		s := stub()
		s.before = func(i int) {
			if i == 1 {
				state.err = fail("internal", "probe failed")
			}
		}
		var got []Record
		e := eachRootOnce(s, c, nil, state, order, []string{"id"}, &relBudget{ctx: ctx}, collect(&got))
		if e == nil || !strings.Contains(e.Error(), "probe failed") {
			t.Fatalf("failure was lost: %v", e)
		}
		relEqual(t, "rows past the failure", len(got), 1)
	})
	t.Run("reports a failure after the last row", func(t *testing.T) {
		state := &existsState{}
		e := eachRootOnce(stub(), c, nil, state, order, []string{"id"}, &relBudget{ctx: ctx}, func(r Record) (bool, error) {
			if r["id"] == "c" {
				state.err = fail("internal", "probe failed")
			}
			return true, nil
		})
		if e == nil || !strings.Contains(e.Error(), "probe failed") {
			t.Fatalf("failure was lost: %v", e)
		}
	})
	t.Run("spends work before a row counts", func(t *testing.T) {
		var got []Record
		e := eachRootOnce(stub(), c, nil, nil, order, []string{"id"}, &relBudget{ctx: ctx, work: relationalWork}, collect(&got))
		var failure *Error
		if !errors.As(e, &failure) || failure.Code != "query_too_expensive" {
			t.Fatalf("work budget: %v", e)
		}
		relEqual(t, "rows past the budget", len(got), 0)
	})
	t.Run("stops at the caller's error", func(t *testing.T) {
		s := stub()
		e := eachRootOnce(s, c, nil, nil, order, []string{"id"}, &relBudget{ctx: ctx}, func(Record) (bool, error) { return true, errors.New("no more") })
		if e == nil || e.Error() != "no more" {
			t.Fatalf("error was lost: %v", e)
		}
		relEqual(t, "reads", s.reads, 1)
	})
	t.Run("stops when the caller has enough", func(t *testing.T) {
		s := stub()
		if e := eachRootOnce(s, c, nil, nil, order, []string{"id"}, &relBudget{ctx: ctx}, func(Record) (bool, error) { return false, nil }); e != nil {
			t.Fatal(e)
		}
		relEqual(t, "reads", s.reads, 1)
	})
	t.Run("reports the scan's own error", func(t *testing.T) {
		s := stub()
		s.err = errors.New("scan failed")
		var got []Record
		e := eachRootOnce(s, c, nil, nil, order, []string{"id"}, &relBudget{ctx: ctx}, collect(&got))
		if e == nil || e.Error() != "scan failed" {
			t.Fatalf("error was lost: %v", e)
		}
	})
	t.Run("uses batches where the adapter doesn't offer it", func(t *testing.T) {
		s := stub()
		s.offers = false
		var got []Record
		if e := eachRootOnce(s, c, nil, nil, order, []string{"id"}, &relBudget{ctx: ctx}, collect(&got)); e != nil {
			t.Fatal(e)
		}
		relEqual(t, "scans", s.reads, 0)
		relEqual(t, "batches", s.finds, 1)
	})
}

// The scan's callback reads the joined collections while a writer is committing.
// RWMutex blocks new readers behind a waiting writer, so any lock held across the
// callback would hang here. Every page must also still see one snapshot.
func TestJoinedPageJoinsDuringItsScan(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	relCollection(rt, "legs", "id", "id text,interaction text,at text", "interaction")
	relCollection(rt, "owners", "id", "id text,name text,generation integer")
	const interactions = 40
	rows, owners := []map[string]any{}, []map[string]any{}
	for i := 0; i < interactions; i++ {
		key := fmt.Sprintf("i%03d", i)
		owners = append(owners, map[string]any{"id": key, "name": "owner-" + key, "generation": "0"})
		for leg := 0; leg < 2; leg++ {
			rows = append(rows, map[string]any{"id": fmt.Sprintf("%s-%d", key, leg), "interaction": key, "at": fmt.Sprintf("2026-%03d", i)})
		}
	}
	relInsert(rt, "legs", rows...)
	relInsert(rt, "owners", owners...)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var writes atomic.Int64
	writerDone := make(chan error, 1)
	go func() {
		for generation := 1; ctx.Err() == nil; generation++ {
			// One write moves both of an interaction's owners and legs together.
			key := fmt.Sprintf("i%03d", generation%interactions)
			var owner, legA, legB Request
			decode(marshal(map[string]any{"collection": "owners", "key": map[string]any{"id": key}, "set": map[string]any{"generation": strconv.Itoa(generation)}}), &owner)
			decode(marshal(map[string]any{"collection": "legs", "key": map[string]any{"id": key + "-0"}, "set": map[string]any{"at": fmt.Sprintf("2027-%06d", generation)}}), &legA)
			decode(marshal(map[string]any{"collection": "legs", "key": map[string]any{"id": key + "-1"}, "set": map[string]any{"at": fmt.Sprintf("2027-%06d", generation)}}), &legB)
			if _, e := rt.m.Execute(ctx, "p", "batch", Request{Database: "d", Operations: []Operation{{"update", owner}, {"update", legA}, {"update", legB}}}); e != nil {
				if ctx.Err() != nil {
					break
				}
				writerDone <- e
				return
			}
			writes.Add(1)
		}
		writerDone <- nil
	}()
	page := `{"collection":"legs","joins":[{"as":"who","collection":"owners","on":[{"local":"root.interaction","foreign":"id"}]}],"group":{"by":["root.interaction"],"orderBy":[{"field":"root.at","direction":"desc","agg":"max"},{"field":"root.interaction"}]},"orderBy":[{"field":"root.id"}],"select":["root.id","root.at","who.name"],"limit":100}`
	done := make(chan error, 1)
	var rounds atomic.Int64
	go func() {
		// Long enough to cross the memory worker's one-second pass as well.
		for deadline := time.Now().Add(2500 * time.Millisecond); time.Now().Before(deadline); {
			var failure *Error
			out, e := relTry(rt, "joined_page", page)
			if errors.As(e, &failure) && (failure.Code == "busy" || failure.Code == "timeout") {
				continue
			}
			if e != nil {
				done <- e
				return
			}
			if fmt.Sprint(out["total"]) != strconv.Itoa(interactions) {
				done <- fmt.Errorf("total %v", out["total"])
				return
			}
			for _, g := range relList(out["groups"]) {
				legs := relList(g["rows"])
				if len(legs) != 2 || legs[0]["root.at"] != legs[1]["root.at"] {
					done <- fmt.Errorf("a page saw a write half way: %v", legs)
					return
				}
				if legs[0]["who.name"] == nil || legs[0]["who.name"] == "" {
					done <- fmt.Errorf("a leg lost its join: %v", legs[0])
					return
				}
			}
			rounds.Add(1)
		}
		done <- nil
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("joined_page hung while joining during its scan")
	}
	stop()
	if e := <-writerDone; e != nil {
		t.Fatal(e)
	}
	if rounds.Load() == 0 || writes.Load() == 0 {
		t.Fatalf("%d pages against %d writes proves nothing", rounds.Load(), writes.Load())
	}
}

func TestJoinedPageStopsWhenCancelled(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	seedVisits(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows := 0
	_, _, e := runVisitPage(t, ctx, rt, fmt.Sprintf(`{"collection":"visits",%s,"limit":5}`, visitShapes["flat"]), func(count *rootReadTx) {
		count.eachRow = func() {
			if rows++; rows == 2 {
				cancel()
			}
		}
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("got %v after %d rows", e, rows)
	}
	if rows > 3 {
		t.Fatalf("read %d rows after being cancelled at the second", rows)
	}
}

// Read batches run on one snapshot, up to four at once.
func TestJoinedPagesInAReadBatch(t *testing.T) {
	rt := relOpen(t, "aptevadb")
	seedVisits(t, rt)
	names := []string{"flat", "max", "pick", "distinct", "inner", "fanout", "aggregate", "left"}
	ops := []Operation{}
	want := []string{}
	for _, name := range names {
		body := fmt.Sprintf(`{"collection":"visits","where":%s,%s,"limit":100}`, visitsOneCentre, visitShapes[name])
		var r Request
		if e := decode([]byte(body), &r); e != nil {
			t.Fatal(e)
		}
		ops = append(ops, Operation{"joined_page", r})
		want = append(want, answerOf(relCall(rt, "joined_page", body)))
	}
	v, e := rt.m.Execute(context.Background(), "p", "batch", Request{Database: "d", Operations: ops})
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		Results []map[string]any `json:"results"`
	}
	if e := decode(marshal(v), &out); e != nil {
		t.Fatal(e)
	}
	relEqual(t, "results", len(out.Results), len(names))
	for i, name := range names {
		relEqual(t, name, answerOf(out.Results[i]), want[i])
	}
}

// BenchmarkJoinedPageWideCollection: sort cost grows with the collection's width when
// each comparison looks its fields up.
func BenchmarkJoinedPageWideCollection(b *testing.B) {
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: "aptevadb"}); e != nil {
		b.Fatal(e)
	}
	fields := []Field{{"id", "integer", false}}
	for i := 0; i < 108; i++ {
		fields = append(fields, Field{fmt.Sprintf("field_%03d", i), "text", true})
	}
	if _, e := m.Execute(ctx, "p", "collection_create", Request{Database: "d", Collection: "wide", Fields: fields, PrimaryKey: []string{"id"}}); e != nil {
		b.Fatal(e)
	}
	const rows = 20000
	for start := 0; start < rows; start += MaxRecords {
		batch := []Record{}
		for i := start; i < start+MaxRecords && i < rows; i++ {
			row := Record{"id": strconv.Itoa(i)}
			for f := 0; f < 108; f++ {
				row[fmt.Sprintf("field_%03d", f)] = fmt.Sprintf("value-%d-%d", i, f)
			}
			batch = append(batch, row)
		}
		if _, e := m.Execute(ctx, "p", "insert", Request{Database: "d", Collection: "wide", Records: batch}); e != nil {
			b.Fatal(e)
		}
	}
	var r Request
	if e := decode([]byte(`{"database":"d","collection":"wide","group":{"by":["root.field_107"],"orderBy":[{"field":"root.field_106","direction":"desc","agg":"max"},{"field":"root.field_107"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":25}`), &r); e != nil {
		b.Fatal(e)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := m.Execute(ctx, "p", "joined_page", r); e != nil {
			b.Fatal(e)
		}
	}
}
