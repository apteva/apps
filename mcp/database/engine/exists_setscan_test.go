package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type setScanFixture struct {
	bookings, staff, tickets, refunds []Record
}

// Identifiers overlap on purpose: a booking's owner_id "5" names staff id 5 and
// also the staff member whose local_id is "5", in another centre.
func newSetScanFixture(tickets, bookings int) setScanFixture {
	rng := rand.New(rand.NewSource(21))
	var f setScanFixture
	centre := func(i int) any {
		if i%17 == 0 {
			return nil
		}
		return fmt.Sprintf("centre-%d", i%8)
	}
	for i := 1; i <= 60; i++ {
		local := any(fmt.Sprintf("agent-%d", i))
		switch i % 6 {
		case 0:
			local = strconv.Itoa(61 - i)
		case 1:
			local = nil
		case 2:
			local = ""
		}
		f.staff = append(f.staff, Record{"native_key": fmt.Sprintf("s%d", i), "id": strconv.Itoa(i), "local_id": local, "centre": centre(rng.Intn(1000))})
	}
	for i := 1; i <= tickets; i++ {
		local := any(fmt.Sprintf("ticket-%d", i))
		if i%50 == 0 {
			local = strconv.Itoa(tickets - i + 1)
		}
		f.tickets = append(f.tickets, Record{"native_key": fmt.Sprintf("t%d", i), "id": strconv.Itoa(i), "local_id": local, "centre": centre(rng.Intn(1000)), "state": fmt.Sprintf("st%d", i%3), "owner_id": strconv.Itoa(1 + rng.Intn(60))})
	}
	for i := 0; i < tickets/4; i++ {
		f.refunds = append(f.refunds, Record{"id": fmt.Sprintf("r%d", i), "ticket_ref": fmt.Sprintf("ticket-%d", 1+rng.Intn(tickets)), "kind": fmt.Sprintf("k%d", i%2)})
	}
	for i := 0; i < bookings; i++ {
		var owner, ticket any
		switch rng.Intn(6) {
		case 0:
		case 1:
			owner = ""
		case 2:
			owner = fmt.Sprintf("agent-%d", 1+rng.Intn(60))
		case 3:
			owner = "007"
		default:
			owner = strconv.Itoa(1 + rng.Intn(60))
		}
		switch rng.Intn(4) {
		case 0:
		case 1:
			ticket = strconv.Itoa(1 + rng.Intn(tickets))
		default:
			ticket = fmt.Sprintf("ticket-%d", 1+rng.Intn(tickets))
		}
		f.bookings = append(f.bookings, Record{"id": fmt.Sprintf("b%04d", i), "n": strconv.Itoa(i), "owner_id": owner, "ticket_id": ticket, "day": fmt.Sprintf("d%d", i%5)})
	}
	return f
}

func setScanIDOrLocal(local string) []ExistsMatch {
	return []ExistsMatch{{On: []ExistsOn{{LocalField: local, ForeignField: "id", Comparison: "text"}}}, {On: []ExistsOn{{LocalField: local, ForeignField: "local_id"}}}}
}

func openSetScan(t testing.TB, adapter string, f setScanFixture) (*Manager, func(string, string, Request) (any, error)) {
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	exec := func(op, coll string, r Request) (any, error) {
		r.Database, r.Collection = "d", coll
		return m.Execute(context.Background(), "p", op, r)
	}
	must := func(op, coll string, r Request) {
		if _, e := exec(op, coll, r); e != nil {
			t.Fatal(op, " ", coll, " ", e)
		}
	}
	if _, e := m.Execute(context.Background(), "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
		t.Fatal(e)
	}
	text := func(n string) Field { return Field{n, "text", true} }
	must("collection_create", "bookings", Request{Fields: []Field{{"id", "text", false}, {"n", "integer", false}, text("owner_id"), text("ticket_id"), text("day")}, PrimaryKey: []string{"id"}})
	must("collection_create", "staff", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, text("local_id"), text("centre")}, PrimaryKey: []string{"native_key"}})
	must("collection_create", "tickets", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, text("local_id"), text("centre"), text("state"), text("owner_id")}, PrimaryKey: []string{"native_key"}})
	must("collection_create", "refunds", Request{Fields: []Field{{"id", "text", false}, text("ticket_ref"), text("kind")}, PrimaryKey: []string{"id"}})
	for _, coll := range []string{"staff", "tickets"} {
		must("index_create", coll, Request{Index: &Index{Name: "by_id", Fields: []Order{{"id", "asc"}}}})
		must("index_create", coll, Request{Index: &Index{Name: "by_local", Fields: []Order{{"local_id", "asc"}}}})
		must("index_create", coll, Request{Index: &Index{Name: "by_centre", Fields: []Order{{"centre", "asc"}, {"id", "asc"}}}})
	}
	must("index_create", "refunds", Request{Index: &Index{Name: "by_ticket", Fields: []Order{{"ticket_ref", "asc"}}}})
	for name, rows := range map[string][]Record{"bookings": f.bookings, "staff": f.staff, "tickets": f.tickets, "refunds": f.refunds} {
		for i := 0; i < len(rows); i += 500 {
			must("insert", name, Request{Records: rows[i:min(i+500, len(rows))]})
		}
	}
	return m, exec
}

func exactRef(v any, row Record) bool {
	return v != nil && (v == row["id"] || row["local_id"] != nil && v == row["local_id"])
}

func anyRelated(rows []Record, v any, test func(Record) bool) bool {
	for _, r := range rows {
		if exactRef(v, r) && test(r) {
			return true
		}
	}
	return false
}

func setScanCases(f setScanFixture) []struct {
	name  string
	where func() *Filter
	want  func(Record) bool
} {
	inCentre := func(names ...any) *Filter { return &Filter{Field: "centre", Op: "in", Value: names} }
	allowed := func(r Record) bool { return r["centre"] == "centre-3" }
	ticketRefunded := func(t Record, kind string) bool {
		for _, r := range f.refunds {
			if t["local_id"] != nil && r["ticket_ref"] == t["local_id"] && r["kind"] == kind {
				return true
			}
		}
		return false
	}
	staffRule := func() Filter {
		return Filter{Exists: &ExistsClause{Collection: "staff", AnyOf: setScanIDOrLocal("owner_id"), Where: inCentre("centre-3")}}
	}
	ticketRule := func(where *Filter) Filter {
		return Filter{Exists: &ExistsClause{Collection: "tickets", AnyOf: setScanIDOrLocal("ticket_id"), Where: where}}
	}
	notRefunded := func() Filter {
		return Filter{NotExists: &ExistsClause{Collection: "refunds", On: []ExistsOn{{LocalField: "local_id", ForeignField: "ticket_ref"}}, Where: &Filter{Field: "kind", Op: "eq", Value: "k0"}}}
	}
	type tc = struct {
		name  string
		where func() *Filter
		want  func(Record) bool
	}
	return []tc{
		{"either branch, each record once", func() *Filter {
			return &Filter{And: []Filter{{Or: []Filter{staffRule(), ticketRule(inCentre("centre-3"))}}, {Field: "day", Op: "gte", Value: "d1"}}}
		}, func(b Record) bool {
			return b["day"] != "d0" && (anyRelated(f.staff, b["owner_id"], allowed) || anyRelated(f.tickets, b["ticket_id"], allowed))
		}},
		{"not_exists with a bound", func() *Filter {
			return &Filter{NotExists: &ExistsClause{Collection: "tickets", AnyOf: setScanIDOrLocal("ticket_id"), Where: inCentre("centre-3")}}
		}, func(b Record) bool { return !anyRelated(f.tickets, b["ticket_id"], allowed) }},
		{"bound beside nested not_exists", func() *Filter {
			return &Filter{And: []Filter{ticketRule(&Filter{And: []Filter{*inCentre("centre-3", "centre-4"), notRefunded()}})}}
		}, func(b Record) bool {
			return anyRelated(f.tickets, b["ticket_id"], func(t Record) bool {
				return (t["centre"] == "centre-3" || t["centre"] == "centre-4") && !ticketRefunded(t, "k0")
			})
		}},
		{"or with a relationship has no bound", func() *Filter {
			return &Filter{And: []Filter{ticketRule(&Filter{Or: []Filter{*inCentre("centre-3"), {Exists: &ExistsClause{Collection: "staff", AnyOf: setScanIDOrLocal("owner_id"), Where: inCentre("centre-5")}}}})}}
		}, func(b Record) bool {
			return anyRelated(f.tickets, b["ticket_id"], func(t Record) bool {
				return allowed(t) || anyRelated(f.staff, t["owner_id"], func(s Record) bool { return s["centre"] == "centre-5" })
			})
		}},
		{"and holding an or", func() *Filter {
			return &Filter{And: []Filter{ticketRule(&Filter{And: []Filter{*inCentre("centre-3", "centre-6"), {Or: []Filter{{Field: "state", Op: "eq", Value: "st1"}, notRefunded()}}}})}}
		}, func(b Record) bool {
			return anyRelated(f.tickets, b["ticket_id"], func(t Record) bool {
				return (t["centre"] == "centre-3" || t["centre"] == "centre-6") && (t["state"] == "st1" || !ticketRefunded(t, "k0"))
			})
		}},
	}
}

// Every strategy returns the same rows, counts and pages as the reference.
func TestKeySetScanStrategiesAgree(t *testing.T) {
	defer func(v int) { existsBuildAfter = v }(existsBuildAfter)
	f := newSetScanFixture(400, 300)
	for _, adapter := range []string{"aptevadb", "pebble", "sqlite"} {
		t.Run(adapter, func(t *testing.T) {
			_, exec := openSetScan(t, adapter, f)
			for _, tc := range setScanCases(f) {
				var want []string
				for i := len(f.bookings) - 1; i >= 0; i-- {
					if tc.want(f.bookings[i]) {
						want = append(want, f.bookings[i]["id"].(string))
					}
				}
				if len(want) == 0 || len(want) == len(f.bookings) {
					t.Fatalf("%s: fixture does not discriminate (%d)", tc.name, len(want))
				}
				for _, mode := range []struct {
					name  string
					after int
				}{{"default", 128}, {"probes only", 1 << 30}, {"forced sets", 64}} {
					existsBuildAfter = mode.after
					var built atomic.Int64
					existsSetsBuilt = func() { built.Add(1) }
					v, e := exec("count", "bookings", Request{Query: Query{Where: tc.where()}})
					if e != nil {
						t.Fatal(tc.name, mode.name, e)
					}
					if c := v.(map[string]any)["count"]; c != strconv.Itoa(len(want)) {
						t.Fatalf("%s / %s: count %v want %d", tc.name, mode.name, c, len(want))
					}
					existsSetsBuilt = nil
					if adapter != "sqlite" && mode.name == "forced sets" && built.Load() == 0 {
						t.Fatalf("%s: no key set was built", tc.name)
					}
					if adapter != "sqlite" && mode.name == "probes only" && built.Load() != 0 {
						t.Fatalf("%s: built a key set with probes only", tc.name)
					}
					var got []string
					q := Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 61, Select: []string{"id", "n"}}
					for {
						v, e := exec("find", "bookings", Request{Query: q})
						if e != nil {
							t.Fatal(tc.name, mode.name, e)
						}
						out := v.(map[string]any)
						for _, r := range out["records"].([]Record) {
							got = append(got, r["id"].(string))
						}
						next, _ := out["nextCursor"].(string)
						if next == "" {
							break
						}
						q.Cursor = next
					}
					if strings.Join(got, ",") != strings.Join(want, ",") {
						t.Fatalf("%s / %s: find\n got %v\nwant %v", tc.name, mode.name, got, want)
					}
				}
			}
		})
	}
}

// A selective indexed predicate reaches the related scan: it reads the matching
// documents, not the collection. Without an index the row allowance still applies.
func TestKeySetScanUsesForeignIndex(t *testing.T) {
	defer func(v int) { existsBuildAfter = v }(existsBuildAfter)
	f := newSetScanFixture(12000, 300)
	matching := 0
	for _, r := range f.tickets {
		if r["centre"] == "centre-3" {
			matching++
		}
	}
	if matching == 0 || matching > 4096 {
		t.Fatalf("fixture has %d matching tickets", matching)
	}
	for _, adapter := range []string{"aptevadb", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			_, exec := openSetScan(t, adapter, f)
			report := func(where *Filter) (int64, map[string]any) {
				t.Helper()
				v, e := exec("count", "bookings", Request{Query: Query{Where: where}, Diagnostics: true})
				if e != nil {
					t.Fatal(e)
				}
				out := v.(map[string]any)
				n, e := strconv.ParseInt(out["count"].(string), 10, 64)
				if e != nil {
					t.Fatal(e)
				}
				return n, out["diagnostics"].(map[string]any)["exists"].(map[string]any)
			}
			existsBuildAfter = 128
			clause := func(where *Filter) *Filter {
				return &Filter{Exists: &ExistsClause{Collection: "tickets", AnyOf: setScanIDOrLocal("ticket_id"), Where: where}}
			}
			_, indexed := report(clause(&Filter{Field: "centre", Op: "in", Value: []any{"centre-3"}}))
			if indexed["keySetsBuilt"] != int64(1) {
				t.Fatalf("indexed: %v", indexed)
			}
			// Index entries lead to documents; pebble also counts the index entries it walks.
			limit := int64(matching) * 2
			if read := indexed["relatedDocumentsExamined"].(int64); read == 0 || read > limit {
				t.Fatalf("read %d related documents for %d matches: %v", read, matching, indexed)
			}
			if read := indexed["relatedRowsRead"].(int64); read != int64(matching) {
				t.Fatalf("returned %d rows for %d matches", read, matching)
			}
			// The index reads most of the collection and the rest of the filter
			// matches nothing. Those rows must count, or nothing could stop the scan.
			everywhere := []any{}
			for i := 0; i < 8; i++ {
				everywhere = append(everywhere, fmt.Sprintf("centre-%d", i))
			}
			_, broad := report(clause(&Filter{And: []Filter{{Field: "centre", Op: "in", Value: everywhere}, {Field: "state", Op: "eq", Value: "none"}}}))
			if broad["keySetsBuilt"] != int64(0) || broad["relatedRowsRead"].(int64) == 0 {
				t.Fatalf("broad index read escaped the allowance: %v", broad)
			}
			// No index on state: the allowance stops the pass, so probes carry on.
			_, unindexed := report(clause(&Filter{Field: "state", Op: "eq", Value: "st1"}))
			if unindexed["keySetsBuilt"] != int64(0) {
				t.Fatalf("unindexed bound escaped the allowance: %v", unindexed)
			}
		})
	}
}

// Key sets last one operation, so the next request sees a changed record.
func TestKeySetsReflectChangesOnNextRequest(t *testing.T) {
	defer func(v int) { existsBuildAfter = v }(existsBuildAfter)
	existsBuildAfter = 64
	f := newSetScanFixture(400, 300)
	for _, adapter := range []string{"aptevadb", "pebble", "sqlite"} {
		t.Run(adapter, func(t *testing.T) {
			_, exec := openSetScan(t, adapter, f)
			where := func() *Filter {
				return &Filter{Exists: &ExistsClause{Collection: "tickets", AnyOf: setScanIDOrLocal("ticket_id"), Where: &Filter{Field: "centre", Op: "eq", Value: "centre-3"}}}
			}
			expected := func(rows []Record) int {
				n := 0
				for _, b := range f.bookings {
					if anyRelated(rows, b["ticket_id"], func(r Record) bool { return r["centre"] == "centre-3" }) {
						n++
					}
				}
				return n
			}
			count := func() string {
				t.Helper()
				v, e := exec("count", "bookings", Request{Query: Query{Where: where()}})
				if e != nil {
					t.Fatal(e)
				}
				return v.(map[string]any)["count"].(string)
			}
			if got := count(); got != strconv.Itoa(expected(f.tickets)) {
				t.Fatalf("before: %s", got)
			}
			// Move every ticket out of the centre.
			rows := make([]Record, len(f.tickets))
			keys := []string{}
			for i, r := range f.tickets {
				copied := Record{}
				for k, v := range r {
					copied[k] = v
				}
				rows[i] = copied
				if r["centre"] == "centre-3" {
					keys = append(keys, r["native_key"].(string))
				}
			}
			sort.Strings(keys)
			if len(keys) < 3 {
				t.Fatalf("%d tickets to move", len(keys))
			}
			if _, e := exec("update", "tickets", Request{Query: Query{Where: &Filter{Field: "centre", Op: "eq", Value: "centre-3"}}, Set: Record{"centre": "centre-9"}, MaxAffected: 1000}); e != nil {
				t.Fatal(e)
			}
			if got := count(); got != "0" {
				t.Fatalf("after removing the permission: %s", got)
			}
		})
	}
}

// Only what the index seeks reaches the scan; the full Where still decides keys.
func TestKeySetScanBound(t *testing.T) {
	c := Collection{Name: "c", PrimaryKey: []string{"id"}, Fields: []Field{{"id", "text", false}, {"a", "text", true}, {"b", "text", true}, {"z", "text", true}},
		Indexes: []Index{{Name: "ab", Fields: []Order{{"a", "asc"}, {"b", "asc"}}}}}
	F := func(field, op string, v any) Filter { return Filter{Field: field, Op: op, Value: v} }
	nested := Filter{NotExists: &ExistsClause{Collection: "other"}}
	all := func(f ...Filter) *Filter { return &Filter{And: f} }
	for _, tc := range []struct {
		name  string
		where *Filter
		want  string
	}{
		{"none", nil, "null"},
		{"exists only", &nested, "null"},
		{"unindexed", all(F("z", "eq", "1"), nested), "null"},
		{"like cannot seek", all(F("a", "contains", "1"), nested), "null"},
		{"or holding a relationship", &Filter{Or: []Filter{F("a", "eq", "1"), nested}}, "null"},
		{"ordinary or", &Filter{Or: []Filter{F("a", "eq", "1"), F("a", "eq", "2")}}, "null"},
		{"second field alone", all(F("b", "eq", "1"), nested), "null"},
		{"equality", all(F("a", "eq", "1"), nested), `{"field":"a","op":"eq","value":"1"}`},
		{"in", all(F("a", "in", []any{"1", "2"}), nested), `{"field":"a","op":"in","value":["1","2"]}`},
		{"compound", all(F("z", "eq", "9"), F("b", "eq", "2"), F("a", "eq", "1"), nested), `{"and":[{"field":"a","op":"eq","value":"1"},{"field":"b","op":"eq","value":"2"}]}`},
		{"range ends the prefix", all(F("a", "gte", "1"), F("b", "eq", "2")), `{"field":"a","op":"gte","value":"1"}`},
		{"inside an and of an and", all(*all(F("a", "eq", "1"), nested), F("z", "eq", "9")), `{"field":"a","op":"eq","value":"1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &existsState{}
			got := (&existsPlan{foreign: c, where: tc.where, state: state}).setScan()
			if text := string(marshal(got.Where)); text != tc.want {
				t.Fatalf("bound %s want %s", text, tc.want)
			}
			if got.examined != &state.examined {
				t.Fatal("scan does not report what it read")
			}
		})
	}
}

// A failed nested read while a bounded set builds keeps no set, and fails.
func TestBoundedSetFailureKeepsNothing(t *testing.T) {
	text := Field{Type: "text"}
	pair := func(local, foreign string) existsAlternative {
		return existsAlternative{pairs: []existsPair{{on: ExistsOn{LocalField: local, ForeignField: foreign}, local: text, foreign: text}}, set: foreign}
	}
	state := &existsState{}
	child := &existsPlan{tx: &failingFind{}, alts: []existsAlternative{pair("id", "parent")}, state: state, noSets: true}
	where := &Filter{And: []Filter{{Field: "centre", Op: "eq", Value: "c"}, {NotExists: &ExistsClause{plan: child}}}}
	parent := &existsPlan{tx: &nestedStub{rows: []Record{{"id": "o1", "owner": "c1", "centre": "c"}}}, foreign: Collection{PrimaryKey: []string{"id"}, Indexes: []Index{{Name: "c", Fields: []Order{{"centre", "asc"}}}}, Fields: []Field{{"centre", "text", true}, {"id", "text", false}, {"owner", "text", true}}}, alts: []existsAlternative{pair("id", "owner")}, where: where, state: state, probes: 1000}
	if e := parent.buildSets(); !errors.Is(e, state.failure()) || e == nil {
		t.Fatalf("failure not returned: %v", e)
	}
	if parent.setScan().Where == nil {
		t.Fatal("the failing scan was not bounded")
	}
	if parent.sets != nil || state.setBytes != 0 {
		t.Fatalf("kept a set: %v, %d bytes", parent.sets, state.setBytes)
	}
}
