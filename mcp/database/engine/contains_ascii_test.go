package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// referenceContains is the requested SQL written out by hand, so no adapter is
// its own reference.
func referenceContains(label any, needle string) bool {
	text, _ := label.(string)
	return strings.Contains(string(asciiLower(nil, text)), string(asciiLower(nil, needle)))
}

func TestContainsASCII(t *testing.T) {
	named := map[string]any{"percent": "Remise 100% ok", "digits": "100 points", "underscore": "x A_B y", "letter": "AxB", "backslash": `a\b`, "accent": "ÉNERGIE", "lower-accent": "énergie", "null": nil, "empty": ""}
	rng := rand.New(rand.NewSource(11))
	pieces := []string{"100", "%", "_", `\`, "A", "b", "é", "É", " ", "Op", "%%", "a_b"}
	var labels []Record
	var parents []Record
	addLabel := func(id string, label any) {
		labels = append(labels, Record{"id": id, "n": fmt.Sprint(len(labels)), "label": label, "centre": fmt.Sprintf("C%d", len(labels)%3)})
	}
	keys := []string{}
	for key := range named {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		addLabel(key, named[key])
	}
	for i := 0; i < 300; i++ {
		var b strings.Builder
		for j := rng.Intn(5); j > 0; j-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		addLabel(fmt.Sprintf("r%03d", i), b.String())
	}
	byID := map[string]any{}
	for _, r := range labels {
		byID[r["id"].(string)] = r["label"]
	}
	for i := 0; i < 400; i++ {
		var ref any
		if i%7 != 0 {
			ref = labels[rng.Intn(len(labels))]["id"]
		}
		parents = append(parents, Record{"id": fmt.Sprintf("p%03d", i), "label_id": ref})
	}
	needles := []string{"100%", "%", "_", "a_b", "A_B", `\`, `a\b`, "REMISE", "énergie", "ÉNERGIE", "", " ", "%%", "missing", "100"}

	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		for _, indexed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/indexed=%v", adapter, indexed), func(t *testing.T) {
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
						t.Fatal(op, " ", e)
					}
					return out
				}
				if _, e := m.Execute(context.Background(), "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
					t.Fatal(e)
				}
				must("collection_create", "labels", Request{PrimaryKey: []string{"id"}, Fields: []Field{{"id", "text", false}, {"n", "integer", false}, {"label", "text", true}, {"centre", "text", true}}})
				must("collection_create", "parents", Request{PrimaryKey: []string{"id"}, Fields: []Field{{"id", "text", false}, {"label_id", "text", true}}})
				if indexed {
					must("index_create", "labels", Request{Index: &Index{Name: "by_label", Fields: []Order{{"label", "asc"}}}})
					must("index_create", "labels", Request{Index: &Index{Name: "by_centre_label", Fields: []Order{{"centre", "asc"}, {"label", "asc"}}}})
				}
				must("insert", "labels", Request{Records: labels})
				must("insert", "parents", Request{Records: parents})

				for _, needle := range needles {
					leaf := func() *Filter { return &Filter{Field: "label", Op: "contains_ascii", Value: needle} }
					var want []string
					perCentre := map[string]int{}
					for i := len(labels) - 1; i >= 0; i-- {
						if referenceContains(labels[i]["label"], needle) {
							want = append(want, labels[i]["id"].(string))
							perCentre[labels[i]["centre"].(string)]++
						}
					}
					// count
					if got := must("count", "labels", Request{Query: Query{Where: leaf()}})["count"]; got != fmt.Sprint(len(want)) {
						t.Fatalf("count %q: %v want %d", needle, got, len(want))
					}
					// find, applied before paging
					var got []string
					q := Query{Where: leaf(), OrderBy: []Order{{"n", "desc"}}, Limit: 23, Select: []string{"id", "n"}}
					for {
						out := must("find", "labels", Request{Query: q})
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
						t.Fatalf("find %q\n got %v\nwant %v", needle, got, want)
					}
					// filtered aggregate, in the query and as a metric's where
					rows := must("aggregate", "labels", Request{Query: Query{Where: leaf()}, GroupBy: []string{"centre"}, Metrics: []Metric{{"n", "count", "", nil}}})["rows"].([]Record)
					total := 0
					for _, r := range rows {
						if fmt.Sprint(r["n"]) != fmt.Sprint(perCentre[r["centre"].(string)]) {
							t.Fatalf("aggregate %q: %v want %v", needle, rows, perCentre)
						}
						total += perCentre[r["centre"].(string)]
					}
					if total != len(want) {
						t.Fatalf("aggregate %q: %v want %v", needle, rows, perCentre)
					}
					gated := must("aggregate", "labels", Request{Metrics: []Metric{{"n", "count", "", leaf()}}})["rows"].([]Record)
					if fmt.Sprint(gated[0]["n"]) != fmt.Sprint(len(want)) {
						t.Fatalf("metric where %q: %v want %d", needle, gated, len(want))
					}
					// beside an indexed scope, and in mixed or
					scoped := 0
					either := 0
					for _, r := range labels {
						hit := referenceContains(r["label"], needle)
						if hit && r["centre"] == "C1" {
							scoped++
						}
						if hit || r["centre"] == "C1" {
							either++
						}
					}
					scope := Filter{Field: "centre", Op: "eq", Value: "C1"}
					if got := must("count", "labels", Request{Query: Query{Where: &Filter{And: []Filter{scope, *leaf()}}}})["count"]; got != fmt.Sprint(scoped) {
						t.Fatalf("and %q: %v want %d", needle, got, scoped)
					}
					if got := must("count", "labels", Request{Query: Query{Where: &Filter{Or: []Filter{scope, *leaf()}}}})["count"]; got != fmt.Sprint(either) {
						t.Fatalf("or %q: %v want %d", needle, got, either)
					}
					// positive and negative exists, by probes and by forced key sets
					related := func(r Record) bool {
						ref, ok := r["label_id"].(string)
						return ok && referenceContains(byID[ref], needle)
					}
					positive, negative := 0, 0
					for _, r := range parents {
						if related(r) {
							positive++
						} else {
							negative++
						}
					}
					clause := func() *ExistsClause {
						return &ExistsClause{Collection: "labels", On: []ExistsOn{{LocalField: "label_id", ForeignField: "id"}}, Where: leaf()}
					}
					defer func(v int) { existsBuildAfter = v }(existsBuildAfter)
					for _, after := range []int{1 << 30, 16} {
						existsBuildAfter = after
						if got := must("count", "parents", Request{Query: Query{Where: &Filter{Exists: clause()}}})["count"]; got != fmt.Sprint(positive) {
							t.Fatalf("exists %q after %d: %v want %d", needle, after, got, positive)
						}
						if got := must("count", "parents", Request{Query: Query{Where: &Filter{NotExists: clause()}}})["count"]; got != fmt.Sprint(negative) {
							t.Fatalf("not_exists %q after %d: %v want %d", needle, after, got, negative)
						}
					}
				}

				// The existing operator keeps its wildcards.
				like := must("count", "labels", Request{Query: Query{Where: &Filter{Fields: []string{"label"}, Op: "like_contains_ascii", Value: "A_B"}}})["count"]
				literal := must("count", "labels", Request{Query: Query{Where: &Filter{Field: "label", Op: "contains_ascii", Value: "A_B"}}})["count"]
				if like == literal {
					t.Fatalf("like_contains_ascii lost its wildcard: %v, literal %v", like, literal)
				}

				for name, where := range map[string]*Filter{
					"number value":  {Field: "label", Op: "contains_ascii", Value: float64(1)},
					"null value":    {Field: "label", Op: "contains_ascii"},
					"list value":    {Field: "label", Op: "contains_ascii", Value: []any{"x"}},
					"integer field": {Field: "n", Op: "contains_ascii", Value: "1"},
					"unknown field": {Field: "nope", Op: "contains_ascii", Value: "1"},
					"too long":      {Field: "label", Op: "contains_ascii", Value: strings.Repeat("x", 1025)},
					"with fields":   {Fields: []string{"label"}, Op: "contains_ascii", Value: "1"},
				} {
					if _, e := exec("count", "labels", Request{Query: Query{Where: where}}); e == nil {
						t.Errorf("%s was accepted", name)
					}
				}
				if _, e := exec("count", "labels", Request{Query: Query{Where: &Filter{Field: "label", Op: "contains_ascii", Value: strings.Repeat("x", 1024)}}}); e != nil {
					t.Errorf("1024 bytes rejected: %v", e)
				}
			})
		}
	}
}
