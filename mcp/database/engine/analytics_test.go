package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A small call-center shape: calls reference campaigns many-to-one. Times are
// chosen around the Europe/Paris switch to summer time on 2026-03-29.
func analyticsFixture(t *testing.T, m *Manager, adapter string) {
	t.Helper()
	call(t, m, "p", "database_create", Request{Database: "dash", Adapter: adapter})
	call(t, m, "p", "collection_create", Request{Database: "dash", Collection: "campaigns", Fields: []Field{{"id", "text", false}, {"name", "text", false}, {"channel", "text", true}}})
	call(t, m, "p", "collection_create", Request{Database: "dash", Collection: "calls", Fields: []Field{{"id", "text", false}, {"campaign_id", "text", true}, {"caller", "text", true}, {"status", "text", false}, {"amount", "number", true}, {"at", "datetime", false}, {"meta", "json", true}}})
	call(t, m, "p", "insert", Request{Database: "dash", Collection: "campaigns", Records: []Record{
		{"id": "c1", "name": "Spring", "channel": "ads"},
		{"id": "c2", "name": "Referral", "channel": nil},
	}})
	call(t, m, "p", "insert", Request{Database: "dash", Collection: "calls", Records: []Record{
		{"id": "1", "campaign_id": "c1", "caller": "alice", "status": "sale", "amount": 100., "at": "2026-03-28T22:30:00Z"},
		{"id": "2", "campaign_id": "c1", "caller": "alice", "status": "missed", "amount": nil, "at": "2026-03-28T23:30:00Z"},
		{"id": "3", "campaign_id": "c1", "caller": "bob", "status": "sale", "amount": 50., "at": "2026-03-29T01:30:00Z"},
		{"id": "4", "campaign_id": "c2", "caller": "carol", "status": "transfer", "amount": nil, "at": "2026-03-29T09:00:00Z"},
		{"id": "5", "campaign_id": "c2", "caller": "carol", "status": "sale", "amount": 25., "at": "2026-03-30T10:00:00Z"},
		{"id": "6", "campaign_id": "gone", "caller": "dave", "status": "sale", "amount": 5., "at": "2026-03-30T11:00:00Z"},
		{"id": "7", "campaign_id": nil, "caller": nil, "status": "missed", "amount": nil, "at": "2026-03-31T12:00:00Z"},
	}})
}

func aggregateRows(t *testing.T, m *Manager, r Request) []Record {
	t.Helper()
	r.Database, r.Collection = "dash", "calls"
	return call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
}

func byKey(t *testing.T, rows []Record, field string) map[any]Record {
	t.Helper()
	out := map[any]Record{}
	for _, r := range rows {
		if _, dup := out[r[field]]; dup {
			t.Fatalf("duplicate group %v in %v", r[field], rows)
		}
		out[r[field]] = r
	}
	return out
}

func TestAnalyticsAdapters(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			analyticsFixture(t, m, adapter)

			t.Run("conditional and distinct metrics", func(t *testing.T) {
				// SQLite compiles these to COUNT(DISTINCT) and FILTER clauses;
				// the other adapters use the portable accumulator.
				got := byKey(t, aggregateRows(t, m, Request{GroupBy: []string{"campaign_id"}, Metrics: []Metric{
					{"calls", "count", "", nil},
					{"callers", "count_distinct", "caller", nil},
					{"sales", "count", "", &Filter{Field: "status", Op: "eq", Value: "sale"}},
					{"selling_callers", "count_distinct", "caller", &Filter{Field: "status", Op: "eq", Value: "sale"}},
					{"revenue", "sum", "amount", &Filter{Field: "status", Op: "in", Value: []any{"sale"}}},
					{"missed_revenue", "sum", "amount", &Filter{Field: "status", Op: "eq", Value: "missed"}},
				}}), "campaign_id")
				want := map[any]Record{
					"c1":   {"calls": "3", "callers": "2", "sales": "2", "selling_callers": "2", "revenue": 150., "missed_revenue": nil},
					"c2":   {"calls": "2", "callers": "1", "sales": "1", "selling_callers": "1", "revenue": 25., "missed_revenue": nil},
					"gone": {"calls": "1", "callers": "1", "sales": "1", "selling_callers": "1", "revenue": 5., "missed_revenue": nil},
					nil:    {"calls": "1", "callers": "0", "sales": "0", "selling_callers": "0", "revenue": nil, "missed_revenue": nil},
				}
				for key, w := range want {
					for name, v := range w {
						if got[key][name] != v {
							t.Fatalf("%v.%s = %#v, want %#v (%v)", key, name, got[key][name], v, got)
						}
					}
				}
			})

			t.Run("lookup groups without duplicating rows", func(t *testing.T) {
				got := byKey(t, aggregateRows(t, m, Request{
					Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id", Select: []string{"name", "channel"}}},
					GroupBy: []string{"campaign.name"},
					Metrics: []Metric{{"calls", "count", "", nil}, {"revenue", "sum", "amount", nil}, {"ads", "count", "", &Filter{Field: "campaign.channel", Op: "eq", Value: "ads"}}},
				}), "campaign.name")
				if len(got) != 3 || got["Spring"]["calls"] != "3" || got["Spring"]["ads"] != "3" || got["Referral"]["calls"] != "2" || got["Referral"]["ads"] != "0" {
					t.Fatal(got)
				}
				// Dangling ("gone") and null references both join to nothing.
				if got[nil]["calls"] != "2" || got[nil]["revenue"] != 5. {
					t.Fatal(got[nil])
				}
				// A lookup may start from an earlier lookup's field.
				chained := byKey(t, aggregateRows(t, m, Request{
					Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id"}, {As: "again", Collection: "campaigns", LocalField: "campaign.id"}},
					GroupBy: []string{"again.name"},
					Metrics: []Metric{{"calls", "count", "", nil}},
				}), "again.name")
				if chained["Spring"]["calls"] != "3" || chained[nil]["calls"] != "2" {
					t.Fatal(chained)
				}
			})

			t.Run("where mixes stored and joined fields", func(t *testing.T) {
				rows := aggregateRows(t, m, Request{
					Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id"}},
					Query:   Query{Where: &Filter{And: []Filter{{Field: "status", Op: "eq", Value: "sale"}, {Field: "campaign.channel", Op: "is_null"}}}},
					Metrics: []Metric{{"sales", "count", "", nil}, {"revenue", "sum", "amount", nil}},
				})
				// Referral (channel null) sale 5, plus the dangling sale 6.
				if len(rows) != 1 || rows[0]["sales"] != "2" || rows[0]["revenue"] != 30. {
					t.Fatal(rows)
				}
			})

			t.Run("calendar buckets in a timezone", func(t *testing.T) {
				day := Bucket{Name: "day", Field: "at", Unit: "day", Timezone: "Europe/Paris"}
				got := byKey(t, aggregateRows(t, m, Request{Buckets: []Bucket{day}, GroupBy: []string{"day"}, Metrics: []Metric{{"calls", "count", "", nil}}, Query: Query{OrderBy: []Order{{"day", "asc"}}}}), "day")
				// 22:30Z on the 28th is 23:30 in Paris; 23:30Z is already the 29th.
				want := map[any]any{"2026-03-28": "1", "2026-03-29": "3", "2026-03-30": "2", "2026-03-31": "1"}
				if len(got) != len(want) {
					t.Fatal(got)
				}
				for k, v := range want {
					if got[k]["calls"] != v {
						t.Fatalf("%v: %v", k, got)
					}
				}
				utc := byKey(t, aggregateRows(t, m, Request{Buckets: []Bucket{{Name: "day", Field: "at", Unit: "day"}}, GroupBy: []string{"day"}, Metrics: []Metric{{"calls", "count", "", nil}}}), "day")
				if utc["2026-03-28"]["calls"] != "2" {
					t.Fatal(utc)
				}
				weeks := aggregateRows(t, m, Request{Buckets: []Bucket{{Name: "week", Field: "at", Unit: "week", Timezone: "Europe/Paris"}, {Name: "month", Field: "at", Unit: "month", Timezone: "Europe/Paris"}}, GroupBy: []string{"week", "month"}, Metrics: []Metric{{"calls", "count", "", nil}}, Query: Query{OrderBy: []Order{{"week", "asc"}}}})
				// Sunday the 29th belongs to the Monday-23 week; Monday the 30th starts the next.
				if len(weeks) != 2 || weeks[0]["week"] != "2026-03-23" || weeks[0]["calls"] != "4" || weeks[1]["week"] != "2026-03-30" || weeks[0]["month"] != "2026-03-01" {
					t.Fatal(weeks)
				}
				sunday := aggregateRows(t, m, Request{Buckets: []Bucket{{Name: "week", Field: "at", Unit: "week", Timezone: "Europe/Paris", WeekStart: "sunday"}}, GroupBy: []string{"week"}, Metrics: []Metric{{"calls", "count", "", nil}}, Query: Query{OrderBy: []Order{{"week", "asc"}}}})
				if len(sunday) != 2 || sunday[0]["week"] != "2026-03-22" || sunday[1]["week"] != "2026-03-29" || sunday[1]["calls"] != "6" {
					t.Fatal(sunday)
				}
				hours := byKey(t, aggregateRows(t, m, Request{Buckets: []Bucket{{Name: "hour", Field: "at", Unit: "hour", Timezone: "Asia/Kolkata"}}, GroupBy: []string{"hour"}, Metrics: []Metric{{"calls", "count", "", nil}}}), "hour")
				// UTC+05:30: 01:30Z is 07:00 local, so its hour starts at 01:30Z.
				if hours["2026-03-29T01:30:00.000000Z"]["calls"] != "1" || hours["2026-03-28T22:30:00.000000Z"]["calls"] != "1" {
					t.Fatal(hours)
				}
			})

			t.Run("distinct days per caller", func(t *testing.T) {
				got := byKey(t, aggregateRows(t, m, Request{Buckets: []Bucket{{Name: "day", Field: "at", Unit: "day", Timezone: "Europe/Paris"}}, GroupBy: []string{"caller"}, Metrics: []Metric{{"active_days", "count_distinct", "day", nil}}}), "caller")
				if got["alice"]["active_days"] != "2" || got["carol"]["active_days"] != "2" {
					t.Fatal(got)
				}
			})

			t.Run("find nests lookups", func(t *testing.T) {
				v := call(t, m, "p", "find", Request{Database: "dash", Collection: "calls", Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id", Select: []string{"name"}}}, Query: Query{Select: []string{"id", "campaign_id"}, OrderBy: []Order{{"id", "asc"}}}})
				recs := rows(t, v)
				if len(recs) != 7 || recs[0]["campaign"].(Record)["name"] != "Spring" || len(recs[0]["campaign"].(Record)) != 1 || recs[5]["campaign"] != nil || recs[6]["campaign"] != nil {
					t.Fatal(recs)
				}
			})

			t.Run("read batch", func(t *testing.T) {
				v := call(t, m, "p", "batch", Request{Database: "dash", Operations: []Operation{
					{"get", Request{Collection: "campaigns", Key: Record{"id": "c1"}}},
					{"count", Request{Collection: "calls"}},
					{"find", Request{Collection: "calls", Query: Query{Limit: 2}}},
					{"aggregate", Request{Collection: "calls", Metrics: []Metric{{"callers", "count_distinct", "caller", nil}}}},
				}}).(map[string]any)["results"].([]any)
				if len(v) != 4 || v[0].(map[string]any)["record"].(Record)["name"] != "Spring" || v[1].(map[string]any)["count"] != "7" || len(rows(t, v[2])) != 2 || v[3].(map[string]any)["rows"].([]Record)[0]["callers"] != "4" {
					t.Fatal(v)
				}
				_, e := m.Execute(context.Background(), "p", "batch", Request{Database: "dash", Operations: []Operation{
					{"count", Request{Collection: "calls"}},
					{"delete", Request{Collection: "calls", Key: Record{"id": "1"}}},
				}})
				expectCode(t, e, "invalid_argument")
				_, e = m.Execute(context.Background(), "p", "batch", Request{Database: "dash", Operations: []Operation{{"collection_drop", Request{Collection: "calls", Confirm: true}}}})
				expectCode(t, e, "invalid_argument")
				if QueryKind("batch", Request{Operations: []Operation{{"find", Request{}}}}) != "read_batch" || QueryKind("batch", Request{Operations: []Operation{{"insert", Request{}}}}) != "batch" {
					t.Fatal("batch kind")
				}
				// A read batch takes the query deadline, a write batch does not.
				if _, e := OperationTimeout("read_batch", 45000); e != nil {
					t.Fatal(e)
				}
				if _, e := OperationTimeout("batch", 45000); e == nil {
					t.Fatal("write batch accepted timeoutMs")
				}
			})

			t.Run("validation", func(t *testing.T) {
				call(t, m, "p", "collection_create", Request{Database: "dash", Collection: "pairs", Fields: []Field{{"a", "text", false}, {"b", "text", false}}, PrimaryKey: []string{"a", "b"}})
				lookup := func(l Lookup) Request {
					return Request{Database: "dash", Collection: "calls", Lookups: []Lookup{l}, Metrics: []Metric{{"n", "count", "", nil}}}
				}
				bad := []Request{
					lookup(Lookup{As: "pair", Collection: "pairs", LocalField: "caller"}),
					lookup(Lookup{As: "campaign", Collection: "campaigns", LocalField: "amount"}),
					lookup(Lookup{As: "status", Collection: "campaigns", LocalField: "campaign_id"}),
					lookup(Lookup{As: "campaign", Collection: "campaigns", LocalField: "campaign_id", Select: []string{"missing"}}),
					{Database: "dash", Collection: "calls", Buckets: []Bucket{{Name: "d", Field: "caller", Unit: "day"}}, Metrics: []Metric{{"n", "count", "", nil}}},
					{Database: "dash", Collection: "calls", Buckets: []Bucket{{Name: "d", Field: "at", Unit: "day", Timezone: "Mars/Olympus"}}, Metrics: []Metric{{"n", "count", "", nil}}},
					{Database: "dash", Collection: "calls", Buckets: []Bucket{{Name: "d", Field: "at", Unit: "day", Timezone: "Local"}}, Metrics: []Metric{{"n", "count", "", nil}}},
					{Database: "dash", Collection: "calls", Buckets: []Bucket{{Name: "d", Field: "at", Unit: "quarter"}}, Metrics: []Metric{{"n", "count", "", nil}}},
					{Database: "dash", Collection: "calls", Buckets: []Bucket{{Name: "d", Field: "at", Unit: "day"}}, Metrics: []Metric{{"n", "sum", "d", nil}}},
					{Database: "dash", Collection: "calls", Metrics: []Metric{{"n", "count_distinct", "meta", nil}}},
					{Database: "dash", Collection: "calls", Metrics: []Metric{{"n", "count", "", &Filter{Field: "nope", Op: "eq", Value: "x"}}}},
					{Database: "dash", Collection: "calls", Metrics: []Metric{{"n", "count", "", nil}}, Query: Query{Where: &Filter{Field: "campaign.name", Op: "eq", Value: "x"}}},
				}
				for i, r := range bad {
					if _, e := m.Execute(context.Background(), "p", "aggregate", r); e == nil {
						t.Fatalf("case %d accepted", i)
					} else {
						expectCode(t, e, "invalid_argument")
					}
				}
				_, e := m.Execute(context.Background(), "p", "find", Request{Database: "dash", Collection: "calls", Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id"}}, Query: Query{Select: []string{"id"}}})
				expectCode(t, e, "invalid_argument")
			})
		})
	}
}

// The native alpha.11 pipeline (lookups, $dateTrunc, filtered and distinct
// accumulators) must agree with the portable accumulator on one snapshot,
// including through the bounded stream used for large group state.
func TestAnalyticsNativeMatchesPortable(t *testing.T) {
	m := manager(t)
	analyticsFixture(t, m, "aptevadb")
	sale := &Filter{Field: "status", Op: "eq", Value: "sale"}
	campaign := Lookup{As: "campaign", Collection: "campaigns", LocalField: "campaign_id"}
	paris := func(unit string) Bucket { return Bucket{Name: unit, Field: "at", Unit: unit, Timezone: "Europe/Paris"} }
	requests := []Request{
		{GroupBy: []string{"campaign_id"}, Metrics: []Metric{{"calls", "count", "", nil}, {"sales", "count", "", sale}, {"callers", "count_distinct", "caller", nil}, {"revenue", "sum", "amount", sale}, {"top", "max", "amount", sale}}},
		{Lookups: []Lookup{campaign}, GroupBy: []string{"campaign.name", "campaign.channel"}, Metrics: []Metric{{"calls", "count", "", nil}, {"ads", "count", "", &Filter{Field: "campaign.channel", Op: "eq", Value: "ads"}}}, Query: Query{OrderBy: []Order{{"calls", "desc"}}}},
		{Lookups: []Lookup{campaign, {As: "again", Collection: "campaigns", LocalField: "campaign.id"}}, GroupBy: []string{"again.name"}, Metrics: []Metric{{"revenue", "sum", "amount", nil}}, Query: Query{Where: &Filter{And: []Filter{*sale, {Field: "campaign.channel", Op: "is_null"}}}}},
		{Buckets: []Bucket{paris("day"), paris("week"), paris("month"), paris("year")}, GroupBy: []string{"day", "week", "month", "year"}, Metrics: []Metric{{"calls", "count", "", nil}}, Query: Query{OrderBy: []Order{{"day", "asc"}}}},
		{Buckets: []Bucket{{Name: "hour", Field: "at", Unit: "hour", Timezone: "Asia/Kolkata"}, {Name: "week", Field: "at", Unit: "week", Timezone: "Europe/Paris", WeekStart: "sunday"}}, GroupBy: []string{"hour", "week"}, Metrics: []Metric{{"calls", "count", "", nil}, {"callers", "count_distinct", "caller", sale}}},
		{Lookups: []Lookup{campaign}, Buckets: []Bucket{paris("day")}, GroupBy: []string{"day", "campaign.name"}, Metrics: []Metric{{"calls", "count", "", nil}, {"callers", "count_distinct", "caller", nil}, {"revenue", "sum", "amount", sale}}},
		{Metrics: []Metric{{"none", "count", "", &Filter{Field: "status", Op: "eq", Value: "absent"}}, {"empty_sum", "sum", "amount", &Filter{Field: "status", Op: "eq", Value: "absent"}}, {"distinct", "count_distinct", "caller", nil}}},
	}
	b := m.databases[func() string {
		for id := range m.databases {
			return id
		}
		return ""
	}()].backend.(*aptevaBackend)
	for i, base := range requests {
		checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
			c, e := tx.collection("calls")
			if e != nil {
				return e
			}
			r := base
			r.Collection = "calls"
			if e := prepareAnalytics(tx, c, &r); e != nil {
				return e
			}
			out, e := validateAggregate(r.analytics.view, &r)
			if e != nil {
				return e
			}
			r.analytics.finish(c, r)
			if !nativeAnalyticsSupported(r) {
				t.Fatalf("case %d left the native path", i)
			}
			at := tx.(*aptevaTx)
			portable, e := at.aggregatePortable(c, r, out)
			if e != nil {
				return e
			}
			native, e := at.aggregateNative(c, r, out)
			if e != nil {
				return fmt.Errorf("case %d native: %w", i, e)
			}
			stream, e := at.aggregateStream(c, r, out, nativeAggregatePlan(c, r))
			if e != nil {
				return fmt.Errorf("case %d stream: %w", i, e)
			}
			for _, rows := range []*[]Record{&portable, &native, &stream} {
				sorted(out, *rows, append(append([]Order{}, r.OrderBy...), groupOrder(r)...))
			}
			if !equalJSON(portable, native) || !equalJSON(portable, stream) {
				t.Fatalf("case %d differs\nportable %s\nnative   %s\nstream   %s", i, marshal(portable), marshal(native), marshal(stream))
			}
			if len(portable) == 0 {
				t.Fatalf("case %d returned nothing", i)
			}
			return nil
		}))
	}
}

func groupOrder(r Request) []Order {
	var out []Order
	for _, g := range r.GroupBy {
		out = append(out, Order{g, "asc"})
	}
	return out
}

// A lookup target too large for the native hash table under the work budget
// falls back to primary-key reads instead of failing the query.
func TestAnalyticsLargeLookupFallsBack(t *testing.T) {
	m := manager(t)
	analyticsFixture(t, m, "aptevadb")
	var campaigns []Record
	pad := strings.Repeat("x", 2000)
	for i := 0; i < 1000; i++ {
		campaigns = append(campaigns, Record{"id": fmt.Sprint("big", i), "name": pad, "channel": "bulk"})
	}
	call(t, m, "p", "insert", Request{Database: "dash", Collection: "campaigns", Records: campaigns})
	for _, d := range m.databases {
		d.backend.(*aptevaBackend).options.QueryWorkBytes = 256 << 10
	}
	rows := aggregateRows(t, m, Request{Lookups: []Lookup{{As: "campaign", Collection: "campaigns", LocalField: "campaign_id"}}, GroupBy: []string{"campaign.name"}, Metrics: []Metric{{"calls", "count", "", nil}}})
	got := byKey(t, rows, "campaign.name")
	if got["Spring"]["calls"] != "3" || got["Referral"]["calls"] != "2" || got[nil]["calls"] != "2" {
		t.Fatal(rows)
	}
}
