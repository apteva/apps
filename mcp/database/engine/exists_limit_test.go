package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A report visible through a linked sale or call, with a search on either link.
// It has the shape of a permission of 8 clauses ANDed with a search of 8.
type limitFixture struct {
	reports, saleLinks, callLinks, sales, calls, offers, prospects []Record
}

func newLimitFixture() limitFixture {
	rng := rand.New(rand.NewSource(31))
	var f limitFixture
	name := func(i int) string {
		return []string{"Offre 100% vert", "Offre A_B", "offre plus", "Gaz"}[i%4]
	}
	for i := 0; i < 8; i++ {
		f.offers = append(f.offers, Record{"id": fmt.Sprintf("o%d", i), "label": name(i)})
	}
	for i := 0; i < 60; i++ {
		f.prospects = append(f.prospects, Record{"id": fmt.Sprintf("x%d", i), "label": name(i + 1)})
	}
	for i := 0; i < 400; i++ {
		f.calls = append(f.calls, Record{"id": fmt.Sprintf("c%d", i), "prospect_id": fmt.Sprintf("x%d", rng.Intn(70))})
	}
	for i := 0; i < 300; i++ {
		f.sales = append(f.sales, Record{"id": fmt.Sprintf("s%d", i), "offer_id": fmt.Sprintf("o%d", rng.Intn(8)), "call_id": fmt.Sprintf("c%d", rng.Intn(450)), "centre": fmt.Sprintf("C%d", rng.Intn(3))})
	}
	for i := 0; i < 500; i++ {
		f.reports = append(f.reports, Record{"id": fmt.Sprintf("r%04d", i), "n": strconv.Itoa(i), "centre": fmt.Sprintf("C%d", i%2)})
	}
	for i := 0; i < 500; i++ {
		f.saleLinks = append(f.saleLinks, Record{"id": fmt.Sprintf("sl%d", i), "report_id": fmt.Sprintf("r%04d", rng.Intn(600)), "sale_ref": fmt.Sprintf("s%d", rng.Intn(330))})
		f.callLinks = append(f.callLinks, Record{"id": fmt.Sprintf("cl%d", i), "report_id": fmt.Sprintf("r%04d", rng.Intn(600)), "call_ref": fmt.Sprintf("c%d", rng.Intn(450))})
	}
	return f
}

func limitE(collection, local, foreign string, where *Filter) Filter {
	return Filter{Exists: &ExistsClause{Collection: collection, On: []ExistsOn{{LocalField: local, ForeignField: foreign}}, Where: where}}
}

func limitN(collection, local, foreign string, where *Filter) Filter {
	return Filter{NotExists: &ExistsClause{Collection: collection, On: []ExistsOn{{LocalField: local, ForeignField: foreign}}, Where: where}}
}

func limitFilter(needle string) *Filter {
	in := func(field string, values ...any) *Filter { return &Filter{Field: field, Op: "in", Value: values} }
	allowed := func() *Filter { return in("offer_id", "o1", "o2", "o3") }
	foreign := func() *Filter { return &Filter{Field: "offer_id", Op: "not_in", Value: []any{"o1", "o2", "o3"}} }
	has := func() *Filter { return &Filter{Field: "label", Op: "contains_ascii", Value: needle} }
	eligible := func(extra ...Filter) *Filter {
		return &Filter{And: append([]Filter{limitE("sales", "id", "call_id", allowed()), limitN("sales", "id", "call_id", foreign())}, extra...)}
	}
	saleLink := func(where *Filter) Filter { return limitE("sale_links", "id", "report_id", where) }
	callLink := func(where *Filter) Filter { return limitE("call_links", "id", "report_id", where) }
	permission := Filter{And: []Filter{
		{Or: []Filter{
			saleLink(&Filter{And: []Filter{limitE("sales", "sale_ref", "id", allowed())}}),
			callLink(&Filter{And: []Filter{limitE("calls", "call_ref", "id", eligible())}}),
		}},
		limitN("sale_links", "id", "report_id", &Filter{And: []Filter{limitE("sales", "sale_ref", "id", foreign())}}),
	}}
	search := Filter{Or: []Filter{
		saleLink(&Filter{And: []Filter{limitE("sales", "sale_ref", "id", &Filter{And: []Filter{*allowed(), limitE("offers", "offer_id", "id", has())}})}}),
		callLink(&Filter{And: []Filter{limitE("calls", "call_ref", "id", eligible(limitE("prospects", "prospect_id", "id", has())))}}),
	}}
	return &Filter{And: []Filter{permission, search}}
}

func countExists(f *Filter) int {
	if f == nil {
		return 0
	}
	n := 0
	for _, c := range []*ExistsClause{f.Exists, f.NotExists} {
		if c != nil {
			n += 1 + countExists(c.Where)
		}
	}
	for i := range f.And {
		n += countExists(&f.And[i])
	}
	for i := range f.Or {
		n += countExists(&f.Or[i])
	}
	return n
}

func (f limitFixture) visible(needle string) (ids []string, perCentre map[string]int) {
	index := func(rows []Record, field string) map[string][]Record {
		out := map[string][]Record{}
		for _, r := range rows {
			out[r[field].(string)] = append(out[r[field].(string)], r)
		}
		return out
	}
	salesByID, salesByCall, callsByID := index(f.sales, "id"), index(f.sales, "call_id"), index(f.calls, "id")
	offersByID, prospectsByID := index(f.offers, "id"), index(f.prospects, "id")
	saleLinks, callLinks := index(f.saleLinks, "report_id"), index(f.callLinks, "report_id")
	allowed := func(s Record) bool { return s["offer_id"] == "o1" || s["offer_id"] == "o2" || s["offer_id"] == "o3" }
	any := func(rows []Record, test func(Record) bool) bool {
		for _, r := range rows {
			if test(r) {
				return true
			}
		}
		return false
	}
	eligible := func(c Record) bool {
		sold := salesByCall[c["id"].(string)]
		return any(sold, allowed) && !any(sold, func(s Record) bool { return !allowed(s) })
	}
	hit := func(rows []Record) bool {
		return any(rows, func(r Record) bool { return referenceContains(r["label"], needle) })
	}
	perCentre = map[string]int{}
	for i := len(f.reports) - 1; i >= 0; i-- {
		r := f.reports[i]
		id := r["id"].(string)
		sl, cl := saleLinks[id], callLinks[id]
		linkedSales := func(test func(Record) bool) bool {
			return any(sl, func(l Record) bool { return any(salesByID[l["sale_ref"].(string)], test) })
		}
		linkedCalls := func(test func(Record) bool) bool {
			return any(cl, func(l Record) bool { return any(callsByID[l["call_ref"].(string)], test) })
		}
		permitted := (linkedSales(allowed) || linkedCalls(eligible)) && !linkedSales(func(s Record) bool { return !allowed(s) })
		found := linkedSales(func(s Record) bool { return allowed(s) && hit(offersByID[s["offer_id"].(string)]) }) ||
			linkedCalls(func(c Record) bool { return eligible(c) && hit(prospectsByID[c["prospect_id"].(string)]) })
		if permitted && found {
			ids = append(ids, id)
			perCentre[r["centre"].(string)]++
		}
	}
	return ids, perCentre
}

func TestExistsClauseLimit(t *testing.T) {
	defer func(v int) { existsBuildAfter = v }(existsBuildAfter)
	f := newLimitFixture()
	if n := countExists(limitFilter("x")); n != 16 {
		t.Fatalf("the filter has %d clauses, want 16", n)
	}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m, e := Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			exec := func(op, coll string, r Request) (map[string]any, error) {
				r.Database, r.Collection = "d", coll
				v, e := m.Execute(context.Background(), "p", op, r)
				if e != nil {
					return nil, e
				}
				out, _ := v.(map[string]any)
				return out, nil
			}
			must := func(op, coll string, r Request) map[string]any {
				t.Helper()
				out, e := exec(op, coll, r)
				if e != nil {
					t.Fatal(op, " ", coll, " ", e)
				}
				return out
			}
			if _, e := m.Execute(context.Background(), "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			text := func(names ...string) []Field {
				out := []Field{{"id", "text", false}}
				for _, n := range names {
					out = append(out, Field{n, "text", true})
				}
				return out
			}
			for _, s := range []struct {
				name    string
				fields  []Field
				indexes []string
				rows    []Record
			}{
				{"reports", append(text("centre"), Field{"n", "integer", false}), nil, f.reports},
				{"sale_links", text("report_id", "sale_ref"), []string{"report_id"}, f.saleLinks},
				{"call_links", text("report_id", "call_ref"), []string{"report_id"}, f.callLinks},
				{"sales", text("offer_id", "call_id", "centre"), []string{"call_id"}, f.sales},
				{"calls", text("prospect_id"), nil, f.calls},
				{"offers", text("label"), nil, f.offers},
				{"prospects", text("label"), nil, f.prospects},
			} {
				must("collection_create", s.name, Request{PrimaryKey: []string{"id"}, Fields: s.fields})
				for _, field := range s.indexes {
					must("index_create", s.name, Request{Index: &Index{Name: "by_" + field, Fields: []Order{{field, "asc"}}}})
				}
				must("insert", s.name, Request{Records: s.rows})
			}
			for _, needle := range []string{"100%", "a_b", "OFFRE", "", "missing"} {
				want, perCentre := f.visible(needle)
				if needle == "100%" && len(want) == 0 {
					t.Fatal("the fixture does not discriminate")
				}
				for _, after := range []int{1 << 30, 16, 128} {
					existsBuildAfter = after
					label := fmt.Sprintf("%q after %d", needle, after)
					if got := must("count", "reports", Request{Query: Query{Where: limitFilter(needle)}})["count"]; got != strconv.Itoa(len(want)) {
						t.Fatalf("count %s: %v want %d", label, got, len(want))
					}
					var got []string
					q := Query{Where: limitFilter(needle), OrderBy: []Order{{"n", "desc"}}, Limit: 19, Select: []string{"id", "n"}}
					for {
						out := must("find", "reports", Request{Query: q})
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
						t.Fatalf("find %s\n got %v\nwant %v", label, got, want)
					}
					rows := must("aggregate", "reports", Request{Query: Query{Where: limitFilter(needle)}, GroupBy: []string{"centre"}, Metrics: []Metric{{"n", "count", "", nil}}})["rows"].([]Record)
					seen := map[string]int{}
					for _, r := range rows {
						n, _ := strconv.Atoi(fmt.Sprint(r["n"]))
						seen[r["centre"].(string)] = n
					}
					if fmt.Sprint(sorted2(seen)) != fmt.Sprint(sorted2(perCentre)) {
						t.Fatalf("aggregate %s: %v want %v", label, seen, perCentre)
					}
				}
			}

			// Exactly the limit passes, and one more is refused with the limit error.
			wide := func(n int) *Filter {
				parts := []Filter{}
				for i := 0; i < n; i++ {
					parts = append(parts, limitE("sale_links", "id", "report_id", nil))
				}
				return &Filter{And: parts}
			}
			must("count", "reports", Request{Query: Query{Where: wide(maxExistsNodes)}})
			want := fmt.Sprintf("at most %d exists filters", maxExistsNodes)
			for _, op := range []string{"count", "find", "aggregate"} {
				r := Request{Query: Query{Where: wide(maxExistsNodes + 1)}}
				if op == "aggregate" {
					r.Metrics = []Metric{{"n", "count", "", nil}}
				}
				_, e := exec(op, "reports", r)
				var ee *Error
				if !errors.As(e, &ee) || !strings.Contains(ee.Message, want) {
					t.Fatalf("%s past the limit: %v", op, e)
				}
			}
			// Metric filters still refuse relationships.
			clause := limitE("sale_links", "id", "report_id", nil)
			if _, e := exec("aggregate", "reports", Request{Metrics: []Metric{{"n", "count", "", &clause}}}); e == nil || !strings.Contains(e.Error(), "metric where cannot use exists") {
				t.Fatalf("metric where: %v", e)
			}
		})
	}
}

func sorted2(m map[string]int) []string {
	out := []string{}
	for k, v := range m {
		if v != 0 {
			out = append(out, fmt.Sprintf("%s=%d", k, v))
		}
	}
	sort.Strings(out)
	return out
}
