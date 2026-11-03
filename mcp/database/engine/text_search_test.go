package engine

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
)

// referenceSearch is SQLite's LIKE over coalesce(a,”)||' '||coalesce(b,”)...
// written as a regexp over ASCII-lowered text.
func referenceSearch(r Record, fields []string, value string) bool {
	parts := make([]string, len(fields))
	for i, f := range fields {
		if v, ok := r[f].(string); ok {
			parts[i] = v
		}
	}
	var re strings.Builder
	re.WriteString("(?s)^")
	for _, c := range string(asciiLower(nil, "%"+value+"%")) {
		switch c {
		case '%':
			re.WriteString(".*")
		case '_':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	return regexp.MustCompile(re.String()).MatchString(string(asciiLower(nil, strings.Join(parts, " "))))
}

func TestLikeContainsASCII(t *testing.T) {
	fields := []string{"resource_key", "opportunity_key", "commercial_id"}
	rng := rand.New(rand.NewSource(3))
	odd := []string{"Café", "50%_off", "a_b", "x\\y", "  pad  ", "ÉTÉ", "Op", ""}
	var records []Record
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 900; i++ {
		r := Record{
			"id":         fmt.Sprintf("p%04d", i),
			"centre":     fmt.Sprintf("C%d", rng.Intn(4)),
			"created_at": base.Add(time.Duration(rng.Intn(180*24)) * time.Hour).Format(time.RFC3339),
			"n":          fmt.Sprint(i),
		}
		vals := []string{fmt.Sprintf("RES-%s-%04d", r["centre"], i), fmt.Sprintf("opp-2026-%03d", rng.Intn(300)), fmt.Sprintf("Commercial %d %s", rng.Intn(40), odd[rng.Intn(len(odd))])}
		for j, f := range fields {
			switch rng.Intn(10) {
			case 0:
				r[f] = nil
			default:
				r[f] = vals[j]
			}
		}
		records = append(records, r)
	}
	values := []string{"", "23 op", "res-c1", "OPP-2026-0", "café", "CAFÉ", "caf_", "50%_", "a_b", "%", "_", "x\\y", "  pad  ", " pad ", "été", "op", "-0012 opp", "zzz", "c1_-0", "é", "1 commercial 3"}
	restrictions := []*Filter{nil, {And: []Filter{{Field: "centre", Op: "eq", Value: "C1"}, {Field: "created_at", Op: "gte", Value: "2026-02-01T00:00:00Z"}, {Field: "created_at", Op: "lt", Value: "2026-05-01T00:00:00Z"}}}}

	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m, e := Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			ctx := context.Background()
			exec := func(op string, r Request) (map[string]any, error) {
				r.Database, r.Collection = "d", "partners"
				v, e := m.Execute(ctx, "p", op, r)
				if e != nil {
					return nil, e
				}
				out, _ := v.(map[string]any)
				return out, nil
			}
			must := func(op string, r Request) map[string]any {
				t.Helper()
				v, e := exec(op, r)
				if e != nil {
					t.Fatal(op, e)
				}
				return v
			}
			if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			must("collection_create", Request{Fields: []Field{{"id", "text", false}, {"centre", "text", false}, {"created_at", "datetime", false}, {"resource_key", "text", true}, {"opportunity_key", "text", true}, {"commercial_id", "text", true}, {"n", "integer", false}}})
			must("index_create", Request{Index: &Index{Name: "by_centre_created", Fields: []Order{{"centre", "asc"}, {"created_at", "asc"}}}})
			for i := 0; i < len(records); i += 300 {
				must("insert", Request{Records: records[i : i+300]})
			}
			for _, value := range values {
				for ri, restriction := range restrictions {
					search := Filter{Fields: fields, Op: opLikeContainsASCII, Value: value}
					where := &search
					if restriction != nil {
						where = &Filter{And: append(append([]Filter{}, restriction.And...), search)}
					}
					var want []string
					wantByCentre := map[string]int{}
					wantByMonth := map[string]int{}
					for i := len(records) - 1; i >= 0; i-- { // n desc
						r := records[i]
						if restriction != nil && (r["centre"] != "C1" || r["created_at"].(string) < "2026-02-01" || r["created_at"].(string) >= "2026-05-01") {
							continue
						}
						if referenceSearch(r, fields, value) {
							want = append(want, r["id"].(string))
							wantByCentre[r["centre"].(string)]++
							wantByMonth[r["created_at"].(string)[:7]]++
						}
					}
					name := fmt.Sprintf("value %q restriction %d", value, ri)

					count := must("count", Request{Query: Query{Where: where}})["count"]
					if count != fmt.Sprint(len(want)) {
						t.Fatalf("%s: count %v want %d", name, count, len(want))
					}
					// Search applies before pagination.
					var got []string
					q := Query{Where: where, OrderBy: []Order{{"n", "desc"}}, Limit: 37}
					for {
						v := must("find", Request{Query: q})
						for _, r := range v["records"].([]Record) {
							got = append(got, r["id"].(string))
						}
						next, _ := v["nextCursor"].(string)
						if next == "" {
							break
						}
						q.Cursor = next
					}
					if strings.Join(got, ",") != strings.Join(want, ",") {
						t.Fatalf("%s: find %d rows, want %d", name, len(got), len(want))
					}
					// Search applies before aggregation, as a filter and as a metric gate.
					rows := must("aggregate", Request{Query: Query{Where: where}, GroupBy: []string{"centre"}, Metrics: []Metric{{"n", "count", "", nil}}})["rows"].([]Record)
					gated := must("aggregate", Request{Query: Query{Where: restriction}, GroupBy: []string{"centre"}, Metrics: []Metric{{"n", "count", "", &search}}})["rows"].([]Record)
					for _, set := range [][]Record{rows, gated} {
						seen := 0
						for _, r := range set {
							if n := r["n"].(string); n != "0" {
								seen++
								if n != fmt.Sprint(wantByCentre[r["centre"].(string)]) {
									t.Fatalf("%s: centre %v count %s want %d", name, r["centre"], n, wantByCentre[r["centre"].(string)])
								}
							}
						}
						if seen != len(wantByCentre) {
							t.Fatalf("%s: %d centres want %d", name, seen, len(wantByCentre))
						}
					}
					months := must("aggregate", Request{Query: Query{Where: where}, Buckets: []Bucket{{Name: "month", Field: "created_at", Unit: "month"}}, GroupBy: []string{"month"}, Metrics: []Metric{{"n", "count", "", nil}}})["rows"].([]Record)
					total := 0
					for _, r := range months {
						var n int
						fmt.Sscan(r["n"].(string), &n)
						total += n
						if month := fmt.Sprint(r["month"])[:7]; n != wantByMonth[month] {
							t.Fatalf("%s: month %s count %d want %d", name, month, n, wantByMonth[month])
						}
					}
					if total != len(want) {
						t.Fatalf("%s: bucketed total %d want %d", name, total, len(want))
					}
					// Read batches evaluate the same predicate.
					batch := must("batch", Request{Operations: []Operation{
						{Op: "count", Request: Request{Database: "d", Collection: "partners", Query: Query{Where: where}}},
						{Op: "find", Request: Request{Database: "d", Collection: "partners", Query: Query{Where: where, OrderBy: []Order{{"n", "desc"}}, Limit: 1000}}},
						{Op: "aggregate", Request: Request{Database: "d", Collection: "partners", Query: Query{Where: restriction}, Metrics: []Metric{{"n", "count", "", &search}}}},
					}})["results"].([]any)
					if c := batch[0].(map[string]any)["count"]; c != fmt.Sprint(len(want)) {
						t.Fatalf("%s: batch count %v", name, c)
					}
					if n := len(batch[1].(map[string]any)["records"].([]Record)); n != len(want) {
						t.Fatalf("%s: batch find %d", name, n)
					}
					if n := batch[2].(map[string]any)["rows"].([]Record)[0]["n"]; n != fmt.Sprint(len(want)) {
						t.Fatalf("%s: batch aggregate %v", name, n)
					}
				}
			}

			// The search never hides the restriction from the planner.
			restricted := &Filter{And: append(append([]Filter{}, restrictions[1].And...), Filter{Fields: fields, Op: opLikeContainsASCII, Value: "op"})}
			plan, e := m.Execute(ctx, "p", "explain", Request{Database: "d", Collection: "partners", Query: Query{Where: restricted, RequireIndex: true}})
			if text := string(marshal(plan)); e != nil || !strings.Contains(text, `"indexedLookup":true`) || adapter != "sqlite" && !strings.Contains(text, `"index":"by_centre_created"`) {
				t.Fatalf("explain: %v %s", e, marshal(plan))
			}
			for _, bad := range []Filter{
				{Fields: []string{"resource_key", "missing"}, Op: opLikeContainsASCII, Value: "x"},
				{Fields: []string{"resource_key", "n"}, Op: opLikeContainsASCII, Value: "x"},
				{Fields: []string{"created_at"}, Op: opLikeContainsASCII, Value: "x"},
				{Fields: []string{}, Op: opLikeContainsASCII, Value: "x"},
				{Fields: fields, Op: opLikeContainsASCII, Value: 3.0},
				{Fields: fields, Op: opLikeContainsASCII},
				{Fields: fields, Op: "eq", Value: "x"},
				{Field: "resource_key", Op: opLikeContainsASCII, Value: "x"},
				{Field: "resource_key", Fields: fields, Op: opLikeContainsASCII, Value: "x"},
				{Fields: fields, Op: opLikeContainsASCII, Value: strings.Repeat("x", 1025)},
				{Fields: []string{"id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id", "id"}, Op: opLikeContainsASCII, Value: "x"},
			} {
				bad := bad
				if _, e := exec("count", Request{Query: Query{Where: &bad}}); e == nil || errCode(e) != "invalid_argument" {
					t.Fatalf("accepted %+v: %v", bad, e)
				}
			}
		})
	}
}

func errCode(e error) string {
	if ee, ok := e.(*Error); ok {
		return ee.Code
	}
	return fmt.Sprint(e)
}

func BenchmarkLikeContainsASCII(b *testing.B) {
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	exec := func(op string, r Request) any {
		r.Database, r.Collection = "d", "partners"
		v, e := m.Execute(ctx, "p", op, r)
		if e != nil {
			b.Fatal(op, e)
		}
		return v
	}
	if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: "aptevadb"}); e != nil {
		b.Fatal(e)
	}
	exec("collection_create", Request{Fields: []Field{{"id", "text", false}, {"centre", "text", false}, {"created_at", "datetime", false}, {"resource_key", "text", true}, {"opportunity_key", "text", true}, {"commercial_id", "text", true}}})
	exec("index_create", Request{Index: &Index{Name: "by_centre_created", Fields: []Order{{"centre", "asc"}, {"created_at", "desc"}}}})
	rng := rand.New(rand.NewSource(4))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 200000; i += 1000 {
		batch := make([]Record, 1000)
		for j := range batch {
			n := i + j
			centre := fmt.Sprintf("C%02d", rng.Intn(30))
			batch[j] = Record{"id": fmt.Sprintf("p%06d", n), "centre": centre, "created_at": base.Add(time.Duration(rng.Intn(365*24)) * time.Hour).Format(time.RFC3339),
				"resource_key": fmt.Sprintf("RES-%s-%06d", centre, n), "opportunity_key": fmt.Sprintf("opp-2026-%05d", rng.Intn(90000)), "commercial_id": fmt.Sprintf("Commercial %d Op", rng.Intn(400))}
		}
		exec("insert", Request{Records: batch})
	}
	restriction := []Filter{{Field: "centre", Op: "eq", Value: "C07"}, {Field: "created_at", Op: "gte", Value: "2026-04-01T00:00:00Z"}, {Field: "created_at", Op: "lt", Value: "2026-07-01T00:00:00Z"}}
	search := Filter{Fields: []string{"resource_key", "opportunity_key", "commercial_id"}, Op: opLikeContainsASCII, Value: "23 op"}
	cases := []struct {
		name  string
		where *Filter
	}{
		{"centre+90d", &Filter{And: restriction}},
		{"centre+90d+search", &Filter{And: append(append([]Filter{}, restriction...), search)}},
		{"all+search", &search},
	}
	for _, tc := range cases {
		b.Run("count/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("count", Request{Query: Query{Where: tc.where}})
			}
		})
		b.Run("find50/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("find", Request{Query: Query{Where: tc.where, OrderBy: []Order{{"created_at", "desc"}}, Limit: 50}})
			}
		})
		b.Run("aggregate/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("aggregate", Request{Query: Query{Where: tc.where}, GroupBy: []string{"centre"}, Metrics: []Metric{{"n", "count", "", nil}}})
			}
		})
	}
}
