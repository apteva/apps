package engine

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// Bodies without values hold literal % signs, so only some are formats.
var relFormat = fmt.Sprintf

type relT struct {
	t *testing.T
	m *Manager
}

func relCall(rt relT, op, args string, values ...any) map[string]any {
	rt.t.Helper()
	out, e := relTry(rt, op, args, values...)
	if e != nil {
		rt.t.Fatalf("%s %s: %v", op, args, e)
	}
	return out
}

func relTry(rt relT, op, args string, values ...any) (map[string]any, error) {
	rt.t.Helper()
	var r Request
	if len(values) > 0 {
		args = relFormat(args, values...)
	}
	if e := decode([]byte(args), &r); e != nil {
		rt.t.Fatalf("decode %s: %v", args, e)
	}
	r.Database = "d"
	v, e := rt.m.Execute(context.Background(), "p", op, r)
	if e != nil {
		return nil, e
	}
	out := map[string]any{}
	if e := decode(marshal(v), &out); e != nil {
		// Some operations answer with a list.
		return map[string]any{}, nil
	}
	return out, nil
}

func relOpen(t *testing.T, adapter string) relT {
	t.Helper()
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	rt := relT{t, m}
	relCall(rt, "database_create", `{"adapter":%q}`, adapter)
	return rt
}

func relCollection(rt relT, name, pk, fields string, indexes ...string) {
	rt.t.Helper()
	list := ""
	for i, part := range splitComma(fields) {
		if i > 0 {
			list += ","
		}
		var fname, ftype string
		fmt.Sscanf(part, "%s %s", &fname, &ftype)
		list += fmt.Sprintf(`{"name":%q,"type":%q,"nullable":%v}`, fname, ftype, fname != pk)
	}
	relCall(rt, "collection_create", `{"collection":%q,"primaryKey":[%q],"fields":[%s]}`, name, pk, list)
	for _, field := range indexes {
		relCall(rt, "index_create", `{"collection":%q,"index":{"name":%q,"fields":[{"field":%q,"direction":"asc"}]}}`, name, "by_"+field, field)
	}
}

func splitComma(s string) []string {
	out, cur := []string{}, ""
	for _, c := range s {
		if c == ',' {
			out, cur = append(out, cur), ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}

func relInsert(rt relT, name string, rows ...map[string]any) {
	rt.t.Helper()
	relCall(rt, "insert", `{"collection":%q,"records":%s}`, name, string(marshal(rows)))
}

func relEach(t *testing.T, fn func(t *testing.T, rt relT)) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) { fn(t, relOpen(t, adapter)) })
	}
}

func relList(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i], _ = item.(map[string]any)
	}
	return out
}

func relStrings(rows []map[string]any, name string) []string {
	out := []string{}
	for _, row := range rows {
		s, _ := row[name].(string)
		out = append(out, s)
	}
	return out
}

func relEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}

// One report: 39 eligible sales, then a mixed-offer call and an allowed call.
func seedRelated(t *testing.T, rt relT) {
	relCollection(rt, "reports", "id", "id text,local_id text,centre text", "centre")
	relCollection(rt, "sales", "id", "id text,report_ref text,centre text,at text,label text,phone text", "report_ref")
	relCollection(rt, "calls", "id", "id text,report_ref text,centre text,at text,label text,phone text", "report_ref")
	relCollection(rt, "call_offers", "id", "id text,call_id text,offer text", "call_id")
	relInsert(rt, "reports",
		map[string]any{"id": "7", "local_id": "rep-a", "centre": "a"},
		map[string]any{"id": "8", "local_id": "rep-empty", "centre": "a"})
	sales := []map[string]any{}
	for i := 1; i <= 39; i++ {
		ref := "rep-a"
		if i%2 == 0 {
			ref = "7" // the same report by its canonical ID
		}
		sales = append(sales, map[string]any{"id": fmt.Sprintf("s%02d", i), "report_ref": ref, "centre": "a", "at": fmt.Sprintf("2026-10-01T00:%02d", i), "label": "Tarif 1000 vert A1B", "phone": "0100000000"})
	}
	// Linked to the report, in another centre; it would match every search.
	sales = append(sales, map[string]any{"id": "s00", "report_ref": "rep-a", "centre": "b", "at": "2026-01-01", "label": "Tarif 100% A_B", "phone": "+33 (0)6 12-34.56.78"})
	relInsert(rt, "sales", sales...)
	relInsert(rt, "calls",
		map[string]any{"id": "c1", "report_ref": "rep-a", "centre": "a", "at": "2026-10-02T00:00", "label": "Mixed 1000 A1B", "phone": "0200000000"},
		map[string]any{"id": "c2", "report_ref": "7", "centre": "a", "at": "2026-10-02T00:01", "label": "Tarif 100% A_B", "phone": "+33 (0)6 12-34.56.78"})
	relInsert(rt, "call_offers",
		map[string]any{"id": "o1", "call_id": "c1", "offer": "allowed"},
		map[string]any{"id": "o2", "call_id": "c1", "offer": "forbidden"},
		map[string]any{"id": "o3", "call_id": "c2", "offer": "allowed"})
}

const relatedLinks = `"anyOf":[{"on":[{"local":"root.local_id","foreign":"report_ref"},{"local":"root.centre","foreign":"centre"}]},{"on":[{"local":"root.id","foreign":"report_ref"},{"local":"root.centre","foreign":"centre"}]}]`

func relatedArgs(extraCallJoin, match string) string {
	return `{"collection":"reports","where":{"field":"id","op":"eq","value":"7"},"count":true,
	  "relation":{"as":"context","limit":40,"sources":[
	    {"name":"sales","joins":[{"as":"x","collection":"sales",` + relatedLinks + `}],
	     "computed":[{"name":"id","expr":{"field":"x.id"}},{"name":"label","expr":{"field":"x.label"}},{"name":"phone","expr":{"strip":{"field":"x.phone"},"chars":" +-.()"}}],
	     "orderBy":[{"field":"x.at"}],"select":["id","label","phone"]},
	    {"name":"calls","joins":[{"as":"x","collection":"calls",` + relatedLinks + `}` + extraCallJoin + `],
	     "computed":[{"name":"id","expr":{"field":"x.id"}},{"name":"label","expr":{"field":"x.label"}},{"name":"phone","expr":{"strip":{"field":"x.phone"},"chars":" +-.()"}}],
	     "orderBy":[{"field":"x.at"}],"select":["id","label","phone"]}
	  ]}` + match + `}`
}

const restrictedOffers = `,{"as":"bad","collection":"call_offers","kind":"anti","on":[{"local":"x.id","foreign":"call_id"}],"where":{"field":"offer","op":"ne","value":"allowed"}}`

func TestRelatedReadBoundedSlots(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		literal := func(v string) string {
			return fmt.Sprintf(`,"match":{"field":"label","op":"contains_ascii","value":%q}`, v)
		}
		phone := `,"match":{"field":"phone","op":"contains_ascii","value":"330612345678"}`

		// Broad: the mixed call takes slot 40, and the allowed call at 41 cannot match.
		broad := relCall(rt, "related_read", relatedArgs("", ""))
		rows := relList(broad["records"])
		if len(rows) != 1 {
			t.Fatalf("broad records: %v", broad)
		}
		context := relList(rows[0]["context"])
		ids := relStrings(context, "id")
		if len(ids) != 40 || ids[0] != "s01" || ids[38] != "s39" || ids[39] != "c1" {
			t.Fatalf("broad context: %v", ids)
		}
		relEqual(t, "slot 40 source", context[39]["source"], "calls")
		relEqual(t, "slot 1 source", context[0]["source"], "sales")
		for _, needle := range []string{"100%", "A_B"} {
			out := relCall(rt, "related_read", relatedArgs("", literal(needle)))
			relEqual(t, "broad search "+needle, relList(out["records"]), []map[string]any{})
			relEqual(t, "broad total "+needle, out["total"], "0")
		}
		relEqual(t, "broad phone", relCall(rt, "related_read", relatedArgs("", phone))["total"], "0")

		// Restricted: the mixed call leaves, and the allowed call moves to slot 40.
		for _, match := range []string{literal("100%"), literal("A_B"), literal("a_b"), phone} {
			out := relCall(rt, "related_read", relatedArgs(restrictedOffers, match))
			relEqual(t, "restricted total "+match, out["total"], "1")
			rows := relList(out["records"])
			if len(rows) != 1 {
				t.Fatalf("restricted %s: %v", match, out)
			}
			context := relList(rows[0]["context"])
			ids := relStrings(context, "id")
			if len(ids) != 40 || ids[39] != "c2" {
				t.Fatalf("restricted context %s: %v", match, ids)
			}
			relEqual(t, "normalised phone", context[39]["phone"], "330612345678")
			for _, id := range ids {
				if id == "s00" || id == "c1" {
					t.Errorf("context kept %s", id)
				}
			}
		}
		// % and _ are literal: "1000" and "A1B" fill every other row, and match neither.
		relEqual(t, "percent alone", relCall(rt, "related_read", relatedArgs(restrictedOffers, literal("%")))["total"], "1")
		relEqual(t, "other-centre phone stays out", relCall(rt, "related_read", relatedArgs("", phone))["hasMore"], false)
		relEqual(t, "no such text", relCall(rt, "related_read", relatedArgs(restrictedOffers, literal("1%0")))["total"], "0")
	})
}

func TestRelatedReadSharedLimitSkipsLaterSources(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		out := relCall(rt, "related_read", `{"collection":"reports","where":{"field":"id","op":"eq","value":"7"},
		  "relation":{"as":"context","limit":3,"sources":[
		    {"name":"sales","joins":[{"as":"x","collection":"sales",`+relatedLinks+`}],"orderBy":[{"field":"x.at","direction":"desc"}],"select":["x.id"]},
		    {"name":"calls","joins":[{"as":"x","collection":"calls",`+relatedLinks+`}],"orderBy":[{"field":"x.at"}],"select":["x.id"]}]}}`)
		relEqual(t, "descending, capped", relStrings(relList(relList(out["records"])[0]["context"]), "x.id"), []string{"s39", "s38", "s37"})
		if _, has := out["total"]; has {
			t.Error("total without count")
		}
	})
}

func TestRelatedReadPagesParents(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		args := `{"collection":"reports","limit":1,"count":true,"orderBy":[{"field":"id"}],%s
		  "relation":{"as":"context","limit":2,"sources":[{"name":"calls","joins":[{"as":"x","collection":"calls",` + relatedLinks + `,"kind":"left"}],"select":["x.id"]}]}}`
		first := relCall(rt, "related_read", args, "")
		relEqual(t, "total", first["total"], "2")
		relEqual(t, "first", relStrings(relList(first["records"]), "id"), []string{"7"})
		relEqual(t, "more", first["hasMore"], true)
		second := relCall(rt, "related_read", args, fmt.Sprintf(`"cursor":%q,`, first["nextCursor"]))
		relEqual(t, "second total", second["total"], "2")
		relEqual(t, "second", relStrings(relList(second["records"]), "id"), []string{"8"})
		relEqual(t, "end", second["hasMore"], false)
		if _, e := relTry(rt, "related_read", args, `"cursor":"bad.bad",`); e == nil {
			t.Error("accepted a forged cursor")
		}
	})
}

// Four interactions in centre a, one with two legs, and rows around them that
// must not count.
func seedInteractions(t *testing.T, rt relT) {
	relCollection(rt, "legs", "id", "id integer,interaction text,centre text,started text,sale_ref text", "centre")
	relCollection(rt, "sales", "id", "id integer,local_id text,centre text,amount integer", "local_id")
	relCollection(rt, "aliases", "id", "id text,leg_id integer,sale_id integer", "leg_id")
	relCollection(rt, "blocked", "id", "id text,interaction text,centre text", "interaction")
	relCall(rt, "index_create", `{"collection":"sales","index":{"name":"by_centre_id","fields":[{"field":"id","direction":"asc"},{"field":"centre","direction":"asc"}]}}`)
	relInsert(rt, "legs",
		map[string]any{"id": "1", "interaction": "i1", "centre": "a", "started": "2026-10-01T09", "sale_ref": "S-1"},
		map[string]any{"id": "2", "interaction": "i1", "centre": "a", "started": "2026-10-01T10", "sale_ref": "01"},
		map[string]any{"id": "3", "interaction": "i2", "centre": "a", "started": "2026-10-02T09", "sale_ref": "2"},
		map[string]any{"id": "4", "interaction": "i3", "centre": "a", "started": "2026-10-02T09", "sale_ref": "3"},
		map[string]any{"id": "5", "interaction": "i4", "centre": "a", "started": "2026-10-03T09", "sale_ref": "none"},
		map[string]any{"id": "6", "interaction": "i5", "centre": "b", "started": "2026-10-01T00", "sale_ref": "S-1"},
		map[string]any{"id": "7", "interaction": "i6", "centre": "a", "started": "2026-10-01T00", "sale_ref": "S-1"})
	relInsert(rt, "sales",
		map[string]any{"id": "1", "local_id": "S-1", "centre": "a", "amount": "100"},
		map[string]any{"id": "2", "local_id": "S-2", "centre": "a", "amount": "200"},
		map[string]any{"id": "3", "local_id": "S-3", "centre": "b", "amount": "300"})
	relInsert(rt, "aliases",
		map[string]any{"id": "a1", "leg_id": "1", "sale_id": "1"},
		map[string]any{"id": "a2", "leg_id": "1", "sale_id": "1"},
		map[string]any{"id": "a3", "leg_id": "2", "sale_id": "1"})
	relInsert(rt, "blocked", map[string]any{"id": "b1", "interaction": "i6", "centre": "a"}, map[string]any{"id": "b2", "interaction": "i2", "centre": "b"})
}

const interactionJoins = `"where":{"field":"centre","op":"eq","value":"a"},"joins":[
  {"as":"blocked","collection":"blocked","kind":"anti","on":[{"local":"root.interaction","foreign":"interaction"},{"local":"root.centre","foreign":"centre"}]},
  {"as":"sale","collection":"sales","kind":"left","anyOf":[
    {"on":[{"local":"root.sale_ref","foreign":"local_id"},{"local":"root.centre","foreign":"centre"}]},
    {"on":[{"local":"root.sale_ref","foreign":"id","comparison":"numeric"},{"local":"root.centre","foreign":"centre"}]}]}]`

const interactionGroups = `{"collection":"legs",` + interactionJoins + `,"distinct":["root","sale"],
  "group":{"by":["root.interaction"],"orderBy":[{"field":"root.started","agg":"min"}]},
  "orderBy":[{"field":"root.started"}],"select":["root.id","root.interaction","sale.id","sale.amount"],"limit":1%s}`

func TestJoinedPageByInteraction(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		seen := []string{}
		cursor := ""
		for page := 0; ; page++ {
			if page > 5 {
				t.Fatal("cursor did not end")
			}
			out := relCall(rt, "joined_page", interactionGroups, cursor)
			relEqual(t, "total", out["total"], "4")
			groups := relList(out["groups"])
			if len(groups) != 1 {
				t.Fatalf("page %d: %v", page, out)
			}
			key := relList([]any{groups[0]["key"]})[0]["root.interaction"].(string)
			seen = append(seen, key)
			rows := relList(groups[0]["rows"])
			switch key {
			case "i1":
				relEqual(t, "both legs in order", relStrings(rows, "root.id"), []string{"1", "2"})
				relEqual(t, "local and numeric reach one sale", relStrings(rows, "sale.id"), []string{"1", "1"})
			case "i3":
				relEqual(t, "other-centre sale is not joined", rows[0]["sale.id"], nil)
			case "i4":
				relEqual(t, "unmatched left join", rows[0]["sale.amount"], nil)
			}
			if out["hasMore"] != true {
				break
			}
			cursor = fmt.Sprintf(`,"cursor":%q`, out["nextCursor"])
		}
		relEqual(t, "ties are stable across pages", seen, []string{"i1", "i2", "i3", "i4"})
		second := relCall(rt, "joined_page", interactionGroups, `,"offset":1`)
		relEqual(t, "offset page", relList(second["groups"])[0]["key"], map[string]any{"root.interaction": "i2"})
		empty := relCall(rt, "joined_page", interactionGroups, `,"offset":10`)
		relEqual(t, "empty page", relList(empty["groups"]), []map[string]any{})
		relEqual(t, "empty total", empty["total"], "4")
		relEqual(t, "empty more", empty["hasMore"], false)
	})
}

func TestJoinedPageAliasesDoNotMultiply(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		args := `{"collection":"legs","where":{"field":"id","op":"lte","value":"2"},"joins":[
		  {"as":"alias","collection":"aliases","on":[{"local":"root.id","foreign":"leg_id"}]},
		  {"as":"sale","collection":"sales","on":[{"local":"alias.sale_id","foreign":"id"}]}],%s
		  "orderBy":[{"field":"sale.id"}],"select":["sale.id"]}`
		relEqual(t, "no dedup: one row per alias", relCall(rt, "joined_page", args, "")["total"], "3")
		relEqual(t, "per leg and sale", relCall(rt, "joined_page", args, `"distinct":["root","sale"],`)["total"], "2")
		one := relCall(rt, "joined_page", args, `"distinct":["sale"],`)
		relEqual(t, "per physical sale", one["total"], "1")
		relEqual(t, "the sale", relStrings(relList(one["records"]), "sale.id"), []string{"1"})
		relEqual(t, "per field", relCall(rt, "joined_page", args, `"distinct":["sale.centre"],`)["total"], "1")
	})
}

func TestJoinComparisonRules(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		count := func(on string) string {
			return relCall(rt, "joined_page", `{"collection":"legs","where":{"field":"id","op":"lte","value":"3"},"joins":[{"as":"s","collection":"sales","on":[`+on+`]}],"select":["root.id"]}`)["total"].(string)
		}
		relEqual(t, "raw matches local text only", count(`{"local":"root.sale_ref","foreign":"local_id"}`), "1")
		relEqual(t, "canonical text is exact: 01 is not 1", count(`{"local":"root.sale_ref","foreign":"id","comparison":"text"}`), "1")
		relEqual(t, "numeric reads 01 as 1", count(`{"local":"root.sale_ref","foreign":"id","comparison":"numeric"}`), "2")
		_, e := relTry(rt, "joined_page", `{"collection":"legs","joins":[{"as":"s","collection":"sales","on":[{"local":"root.sale_ref","foreign":"id"}]}]}`)
		if e == nil {
			t.Error("compared text and integer without a rule")
		}
		_, e = relTry(rt, "joined_page", `{"collection":"legs","joins":[{"as":"s","collection":"sales","on":[{"local":"root.sale_ref","foreign":"amount","comparison":"numeric"}]}]}`)
		if e == nil {
			t.Error("joined without an index")
		}
		// Existence: only interactions with a block in their own centre.
		semi := relCall(rt, "joined_page", `{"collection":"legs","joins":[{"as":"b","collection":"blocked","kind":"semi","on":[{"local":"root.interaction","foreign":"interaction"},{"local":"root.centre","foreign":"centre"}]}],"select":["root.id"]}`)
		relEqual(t, "semi", relStrings(relList(semi["records"]), "root.id"), []string{"7"})
	})
}

// One caller under two advisers and two campaigns.
func seedAttribution(t *testing.T, rt relT) {
	relCollection(rt, "campaigns", "id", "id text,fixed_cost integer")
	relCollection(rt, "calls", "id", "id text,caller text,adviser text,campaign text,at text", "caller")
	relCollection(rt, "contracts", "id", "id text,caller text,kind text,at text,amount integer", "caller")
	relCall(rt, "index_create", `{"collection":"calls","index":{"name":"by_campaign","fields":[{"field":"campaign","direction":"asc"}]}}`)
	relInsert(rt, "campaigns",
		map[string]any{"id": "fixed", "fixed_cost": "1001"},
		map[string]any{"id": "missing", "fixed_cost": nil},
		map[string]any{"id": "nocontracts", "fixed_cost": "500"},
		map[string]any{"id": "free", "fixed_cost": "0"})
	relInsert(rt, "calls",
		map[string]any{"id": "c1", "caller": "+33600", "adviser": "ann", "campaign": "fixed", "at": "2026-10-01"},
		map[string]any{"id": "c2", "caller": "+33600", "adviser": "bob", "campaign": "fixed", "at": "2026-10-02"},
		map[string]any{"id": "c3", "caller": "+33600", "adviser": "ann", "campaign": "missing", "at": "2026-10-03"},
		map[string]any{"id": "c4", "caller": "+33601", "adviser": "ann", "campaign": "free", "at": "2026-10-04"})
	relInsert(rt, "contracts",
		map[string]any{"id": "k1", "caller": "+33600", "kind": "energy", "at": "2026-10-05", "amount": "9007199254740993"},
		map[string]any{"id": "k2", "caller": "+33600", "kind": "energy", "at": "2026-10-06", "amount": "1"},
		map[string]any{"id": "k3", "caller": "+33600", "kind": "addon", "at": "2026-10-07", "amount": "5"},
		map[string]any{"id": "k4", "caller": "+33600", "kind": "callback", "at": "2026-10-08", "amount": nil},
		map[string]any{"id": "k5", "caller": "+33601", "kind": "energy", "at": "2026-11-01", "amount": "7"})
}

func relByKey(rows []map[string]any, key string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, row := range rows {
		k, _ := row[key].(string)
		out[k] = row
	}
	return out
}

func TestJoinedAggregateAttribution(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedAttribution(t, rt)
		global := relCall(rt, "joined_aggregate", `{"collection":"calls","where":{"field":"caller","op":"eq","value":"+33600"},"metrics":[{"name":"callers","op":"count_distinct","field":"root.caller"},{"name":"calls","op":"count"}]}`)
		row := relList(global["rows"])[0]
		relEqual(t, "one global caller", row["callers"], "1")
		relEqual(t, "calls", row["calls"], "3")
		perCampaign := relByKey(relList(relCall(rt, "joined_aggregate", `{"collection":"calls","where":{"field":"caller","op":"eq","value":"+33600"},"groupBy":["root.campaign"],"metrics":[{"name":"callers","op":"count_distinct","field":"root.caller"},{"name":"calls","op":"count"}]}`)["rows"]), "root.campaign")
		relEqual(t, "fixed callers", perCampaign["fixed"]["callers"], "1")
		relEqual(t, "fixed calls", perCampaign["fixed"]["calls"], "2")
		relEqual(t, "missing callers", perCampaign["missing"]["callers"], "1")

		// Each contract to its caller's latest October call, then counted once.
		attributed := func(which string) map[string]map[string]any {
			return relByKey(relList(relCall(rt, "joined_aggregate", `{"collection":"contracts",
			  "where":{"and":[{"field":"at","op":"gte","value":"2026-10-01"},{"field":"at","op":"lt","value":"2026-11-01"}]},
			  "joins":[{"as":"call","collection":"calls","on":[{"local":"root.caller","foreign":"caller"}]}],
			  "pick":{"by":["root.id"],"orderBy":[{"field":"call.at","direction":%q}]},
			  "groupBy":["call.campaign"],
			  "metrics":[{"name":"energy","op":"count","where":{"field":"root.kind","op":"eq","value":"energy"}},
			    {"name":"addons","op":"count","where":{"field":"root.kind","op":"eq","value":"addon"}},
			    {"name":"money","op":"sum","field":"root.amount","where":{"field":"root.kind","op":"eq","value":"energy"}},
			    {"name":"callback_money","op":"sum","field":"root.amount","where":{"field":"root.kind","op":"eq","value":"callback"}}]}`, which)["rows"]), "call.campaign")
		}
		latest := attributed("desc")
		relEqual(t, "two contracts, latest call", latest["missing"]["energy"], "2")
		relEqual(t, "separate add-on", latest["missing"]["addons"], "1")
		relEqual(t, "exact money", latest["missing"]["money"], "9007199254740994")
		relEqual(t, "null, not zero", latest["missing"]["callback_money"], nil)
		if _, has := latest["free"]; has {
			t.Error("the November contract crossed the end boundary")
		}
		first := attributed("asc")
		relEqual(t, "first call", first["fixed"]["energy"], "2")
		if _, has := first["missing"]; has {
			t.Error("first attribution also counted the later campaign")
		}

		// Fixed cost survives the join once; a zero denominator and a missing cost are null.
		costs := relByKey(relList(relCall(rt, "joined_aggregate", `{"collection":"campaigns",
		  "joins":[{"as":"call","collection":"calls","kind":"left","on":[{"local":"root.id","foreign":"campaign"}]},
		    {"as":"contract","collection":"contracts","kind":"left","on":[{"local":"call.caller","foreign":"caller"}],"where":{"field":"kind","op":"eq","value":"energy"}}],
		  "distinct":["root","contract"],
		  "groupBy":["root.id"],
		  "metrics":[{"name":"cost","op":"max","field":"root.fixed_cost"},{"name":"contracts","op":"count_distinct","field":"contract.id"}],
		  "derived":[{"name":"per","op":"divide","numerator":"cost","denominator":"contracts","round":2},{"name":"whole","op":"divide","numerator":"cost","denominator":"contracts","round":0}]}`)["rows"]), "root.id")
		relEqual(t, "fixed cost", costs["fixed"]["cost"], "1001")
		relEqual(t, "fixed contracts", costs["fixed"]["contracts"], "2")
		relEqual(t, "cost per contract", fmt.Sprint(costs["fixed"]["per"]), "500.5")
		relEqual(t, "rounded half up", fmt.Sprint(costs["fixed"]["whole"]), "501")
		relEqual(t, "missing cost", costs["missing"]["per"], nil)
		relEqual(t, "zero denominator contracts", costs["nocontracts"]["contracts"], "0")
		relEqual(t, "zero denominator", costs["nocontracts"]["per"], nil)
		relEqual(t, "zero cost is zero, not null", fmt.Sprint(costs["free"]["per"]), "0")
	})
}

func TestRelationalReadsShareOneSnapshotBatch(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		out := relCall(rt, "batch", `{"operations":[
		  {"op":"joined_page","args":{"collection":"legs",`+interactionJoins+`,"distinct":["root"],"select":["root.id"],"limit":1}},
		  {"op":"joined_aggregate","args":{"collection":"legs",`+interactionJoins+`,"metrics":[{"name":"n","op":"count_distinct","field":"root.interaction"}]}}]}`)
		results := relList(out["results"])
		relEqual(t, "row grain total", results[0]["total"], "5")
		relEqual(t, "interactions", relList(results[1]["rows"])[0]["n"], "4")
	})
}

func TestRelationalBudgetsFailWholeRead(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		old := relationalWork
		relationalWork = 3
		defer func() { relationalWork = old }()
		_, e := relTry(rt, "joined_page", `{"collection":"legs",`+interactionJoins+`}`)
		if e == nil {
			t.Fatal("exceeded the work budget without an error")
		}
		relationalWork = old
		oldRetained := relationalRetained
		relationalRetained = 64
		defer func() { relationalRetained = oldRetained }()
		if _, e := relTry(rt, "joined_page", `{"collection":"legs",`+interactionJoins+`}`); e == nil {
			t.Fatal("exceeded the memory budget without an error")
		}
	})
}

func TestRelationalComputedExpressions(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "people", "id", "id text,name text,phone text,meta text,empty text")
		relInsert(rt, "people", map[string]any{"id": "1", "name": "  Ann ", "phone": "+33 (0)6.12-34", "meta": `{"age":41,"tags":{"a":1}}`, "empty": ""})
		out := relCall(rt, "joined_page", `{"collection":"people","computed":[
		  {"name":"trimmed","expr":{"trim":{"field":"root.name"}}},
		  {"name":"digits","expr":{"strip":{"field":"root.phone"},"chars":" +-.()"}},
		  {"name":"fallback","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.empty"}},{"const":"none"}]}},
		  {"name":"label","expr":{"concat":[{"field":"trimmed"},{"field":"root.empty"},{"field":"digits"}],"separator":"|"}},
		  {"name":"age","type":"integer","expr":{"json":{"field":"root.meta"},"path":"age"}},
		  {"name":"missing","expr":{"json":{"field":"root.meta"},"path":"tags.b"}},
		  {"name":"adult","expr":{"if":{"field":"age","op":"gte","value":"18"},"then":{"const":"yes"},"else":{"const":"no"}}}],
		  "filter":{"field":"age","op":"gt","value":"40"},"select":["trimmed","digits","fallback","label","age","missing","adult"]}`)
		rows := relList(out["records"])
		if len(rows) != 1 {
			t.Fatalf("computed row: %v", out)
		}
		relEqual(t, "computed", rows[0], map[string]any{"trimmed": "Ann", "digits": "33061234", "fallback": "none", "label": "Ann||33061234", "age": "41", "missing": nil, "adult": "yes"})
		for label, args := range map[string]string{
			"two forms":       `{"collection":"people","computed":[{"name":"x","expr":{"field":"root.id","const":"a"}}]}`,
			"unknown field":   `{"collection":"people","computed":[{"name":"x","expr":{"field":"root.nope"}}]}`,
			"name clash":      `{"collection":"people","computed":[{"name":"root","expr":{"const":"a"}}]}`,
			"exists in view":  `{"collection":"people","filter":{"exists":{"collection":"people","on":[{"localField":"id","foreignField":"id"}]}}}`,
			"bad kind":        `{"collection":"people","joins":[{"as":"x","collection":"people","kind":"cross","on":[{"local":"root.id","foreign":"id"}]}]}`,
			"bad alias":       `{"collection":"people","joins":[{"as":"root","collection":"people","on":[{"local":"root.id","foreign":"id"}]}]}`,
			"both on":         `{"collection":"people","joins":[{"as":"x","collection":"people","on":[{"local":"root.id","foreign":"id"}],"anyOf":[{"on":[{"local":"root.id","foreign":"id"}]}]}]}`,
			"cursor + offset": `{"collection":"people","cursor":"a.b","offset":1}`,
			"bad group order": `{"collection":"people","group":{"by":["root.id"],"orderBy":[{"field":"root.name"}]}}`,
		} {
			if _, e := relTry(rt, "joined_page", args); e == nil {
				t.Errorf("%s was accepted", label)
			}
		}
		if _, e := relTry(rt, "joined_page", `{"collection":"people","computed":[{"name":"x","type":"integer","expr":{"field":"root.name"}}]}`); e == nil {
			t.Error("a value that is not its declared type gave no error")
		}
	})
}

func TestRelationalTiesFollowThePhysicalKey(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		// Odd sales link by local ID and even sales by canonical ID, so without the
		// tie-break the first alternative's rows would all come first.
		out := relCall(rt, "related_read", `{"collection":"reports","where":{"field":"id","op":"eq","value":"7"},
		  "relation":{"as":"context","limit":3,"sources":[{"name":"sales","joins":[{"as":"x","collection":"sales",`+relatedLinks+`}],"orderBy":[{"field":"x.centre"}],"select":["x.id"]}]}}`)
		relEqual(t, "ties", relStrings(relList(relList(out["records"])[0]["context"]), "x.id"), []string{"s01", "s02", "s03"})
	})
}

func TestJoinAlternativesFindARecordOnce(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,ref text")
		relCollection(rt, "sales", "id", "id integer,local_id text", "local_id")
		relCall(rt, "index_create", `{"collection":"sales","index":{"name":"by_id","fields":[{"field":"id","direction":"asc"}]}}`)
		relInsert(rt, "legs", map[string]any{"id": "l1", "ref": "4"})
		relInsert(rt, "sales", map[string]any{"id": "4", "local_id": "4"})
		out := relCall(rt, "joined_page", `{"collection":"legs","joins":[{"as":"s","collection":"sales","anyOf":[{"on":[{"local":"root.ref","foreign":"local_id"}]},{"on":[{"local":"root.ref","foreign":"id","comparison":"numeric"}]}]}],"select":["s.id"]}`)
		relEqual(t, "both alternatives, one record", out["total"], "1")
	})
}

func TestRelatedReadCursorWithMatch(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		relInsert(rt, "reports", map[string]any{"id": "9", "local_id": "rep-c", "centre": "a"})
		relInsert(rt, "calls", map[string]any{"id": "c9", "report_ref": "rep-c", "centre": "a", "at": "2026-10-03", "label": "Autre 100%", "phone": "03"})
		args := `{"collection":"reports","limit":1,"orderBy":[{"field":"id"}],%s
		  "relation":{"as":"context","limit":50,"sources":[{"name":"calls","joins":[{"as":"x","collection":"calls",` + relatedLinks + `}],"computed":[{"name":"label","expr":{"field":"x.label"}}],"select":["label"]}]},
		  "match":{"field":"label","op":"contains_ascii","value":"100%%"}}`
		walk := func(extra string) ([]string, []any) {
			ids, totals := []string{}, []any{}
			cursor := ""
			for range 4 {
				out := relCall(rt, "related_read", args, extra+cursor)
				ids = append(ids, relStrings(relList(out["records"]), "id")...)
				totals = append(totals, out["total"])
				if out["hasMore"] != true {
					return ids, totals
				}
				cursor = fmt.Sprintf(`"cursor":%q,`, out["nextCursor"])
			}
			t.Fatal("cursor did not end")
			return nil, nil
		}
		ids, totals := walk("")
		relEqual(t, "cursor without count", ids, []string{"7", "9"})
		relEqual(t, "no total asked", totals, []any{nil, nil})
		ids, totals = walk(`"count":true,`)
		relEqual(t, "cursor with count", ids, []string{"7", "9"})
		relEqual(t, "total before and after the cursor", totals, []any{"2", "2"})
		second := relCall(rt, "related_read", args, `"offset":1,"count":true,`)
		relEqual(t, "offset", relStrings(relList(second["records"]), "id"), []string{"9"})
		relEqual(t, "offset total", second["total"], "2")
	})
}

// Each sale tests one rule of an attribution with two branches: explicit call
// origins (its own, or its parent sale's) and a one-adviser fallback.
func seedBranches(t *testing.T, rt relT) {
	relCollection(rt, "calls", "id", "id integer,prospect text,handled text,adviser text,campaign text,energy integer", "id", "prospect")
	relCollection(rt, "sale", "id", "id integer,local_id text,family text,prospect text,appel text,parent text", "id", "local_id")
	relCollection(rt, "origins", "id", "id integer,sale_id integer,call_id integer", "sale_id")
	relCollection(rt, "bookings", "id", "id integer,prospect text,kind text", "prospect")
	call := func(id, prospect, handled, adviser, campaign, energy string) map[string]any {
		return map[string]any{"id": id, "prospect": prospect, "handled": handled, "adviser": adviser, "campaign": campaign, "energy": energy}
	}
	relInsert(rt, "calls",
		call("1", "p1", "human", "ann", "A", "1"),
		call("2", "p2", "human", "ann", "A", "1"), call("3", "p2", "human", "bob", "B", "1"),
		call("4", "p3", "human", "ann", "B", "1"),
		call("5", "p4", "human", "ann", "A", "1"), call("6", "p4", "AI", "", "A", "1"),
		call("7", "p5", "ai", "", "A", "1"), call("8", "p5", "human", "ann", "A", "1"),
		call("60", "p6", "human", "cat", "C", "1"), call("61", "p6", "human", "cat", "C", "1"),
		call("70", "p7", "human", "ann", "D", "1"), call("71", "p7", "human", "bob", "D", "0"),
		call("80", "p8", "human", "ann", "E", "1"),
		call("90", "p9", "human", "ann", "G", "1"), call("91", "p9", "human", "ann", "H", "1"),
		call("100", "p10", "human", "ann", "F", "1"),
		call("110", "p11", "human", "ann", "J", "0"))
	sale := func(id, prospect, appel, parent string) map[string]any {
		row := map[string]any{"id": id, "local_id": "S" + id, "family": "energy", "prospect": prospect, "appel": nil, "parent": nil}
		if appel != "" {
			row["appel"] = appel
		}
		if parent != "" {
			row["parent"] = parent
		}
		return row
	}
	addon := sale("120", "p1", "x", "")
	addon["family"] = "addon"
	relInsert(rt, "sale",
		sale("1", "p1", "x", ""),    // A
		sale("2", "p2", "x", ""),    // two campaigns: unattributed
		sale("30", "p3", "x", ""),   // parent of 3, and itself B
		sale("3", "p3", "x", "S30"), // B, through its parent by local ID
		sale("4", "p4", "x", ""),    // an AI origin excludes it
		sale("50", "p5", "x", ""),   // an AI origin excludes it
		sale("5", "p5", "x", "50"),  // excluded through its parent, by canonical ID
		sale("6", "p6", "", ""),     // fallback: C
		sale("7", "p7", "", ""),     // two advisers, one on a non-energy call
		sale("8", "p8", "", ""),     // an AI booking
		sale("9", "p9", "", ""),     // origin G, fallback G and H: unattributed
		sale("10", "p10", "", ""),   // origin F and fallback F: once
		sale("11", "p11", "", ""),   // no energy call
		addon)
	relInsert(rt, "origins",
		map[string]any{"id": "1", "sale_id": "1", "call_id": "1"},
		map[string]any{"id": "2", "sale_id": "2", "call_id": "2"}, map[string]any{"id": "3", "sale_id": "2", "call_id": "3"},
		map[string]any{"id": "4", "sale_id": "30", "call_id": "4"},
		map[string]any{"id": "5", "sale_id": "4", "call_id": "5"}, map[string]any{"id": "6", "sale_id": "4", "call_id": "6"},
		map[string]any{"id": "7", "sale_id": "50", "call_id": "7"}, map[string]any{"id": "8", "sale_id": "5", "call_id": "8"},
		map[string]any{"id": "9", "sale_id": "9", "call_id": "90"},
		map[string]any{"id": "10", "sale_id": "10", "call_id": "100"})
	relInsert(rt, "bookings", map[string]any{"id": "1", "prospect": "p8", "kind": "ai_booking"}, map[string]any{"id": "2", "prospect": "p6", "kind": "human"})
}

const (
	branchHuman  = `{"field":"handled","op":"not_in","value":["ai","AI"]}`
	branchEnergy = `{"and":[` + branchHuman + `,{"field":"energy","op":"eq","value":"1"}]}`
	branchOutput = `"computed":[{"name":"sale","expr":{"field":"root.id"}},{"name":"campaign","expr":{"field":"h.campaign"}}],"select":["sale","campaign"]`
	branchLinks  = `"anyOf":[{"on":[{"local":"root.id","foreign":"sale_id"}]},{"on":[{"local":"p.id","foreign":"sale_id"}]}]`
	branchPlan   = `"branches":[
	  {"collection":"sale","where":{"field":"family","op":"eq","value":"energy"},"joins":[
	    {"as":"p","collection":"sale","kind":"left","anyOf":[{"on":[{"local":"root.parent","foreign":"local_id"}]},{"on":[{"local":"root.parent","foreign":"id","comparison":"text"}]}]},
	    {"as":"ai","collection":"origins","kind":"anti",` + branchLinks + `,"where":{"exists":{"collection":"calls","on":[{"localField":"call_id","foreignField":"id"}],"where":{"field":"handled","op":"in","value":["ai","AI"]}}}},
	    {"as":"o","collection":"origins",` + branchLinks + `},
	    {"as":"h","collection":"calls","on":[{"local":"o.call_id","foreign":"id"},{"local":"root.prospect","foreign":"prospect"}],"where":` + branchEnergy + `}],` + branchOutput + `},
	  {"collection":"sale","where":{"and":[{"field":"family","op":"eq","value":"energy"},{"field":"appel","op":"is_null"}]},"joins":[
	    {"as":"booked","collection":"bookings","kind":"anti","on":[{"local":"root.prospect","foreign":"prospect"}],"where":{"field":"kind","op":"eq","value":"ai_booking"}},
	    {"as":"reach","collection":"calls","kind":"aggregate","on":[{"local":"root.prospect","foreign":"prospect"}],"where":` + branchHuman + `,"metrics":[{"name":"advisers","op":"count_distinct","field":"adviser"}]},
	    {"as":"h","collection":"calls","on":[{"local":"root.prospect","foreign":"prospect"}],"where":` + branchEnergy + `}],
	   "filter":{"field":"reach.advisers","op":"eq","value":"1"},` + branchOutput + `}],
	  "distinct":["sale","campaign"],
	  "rollup":{"by":["sale"],"metrics":[{"name":"campaigns","op":"count_distinct","field":"campaign"},{"name":"only","op":"min","field":"campaign"}],
	    "computed":[{"name":"campaign","type":"text","expr":{"if":{"field":"campaigns","op":"eq","value":"1"},"then":{"field":"only"},"else":{"const":"unattributed"}}}]}`
)

func TestJoinedAggregateBranchesAndRollup(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedBranches(t, rt)
		out := relCall(rt, "joined_aggregate", `{`+branchPlan+`,"groupBy":["campaign"],"metrics":[{"name":"contracts","op":"count_distinct","field":"sale"}]}`)
		got := map[string]any{}
		for _, row := range relList(out["rows"]) {
			got[row["campaign"].(string)] = row["contracts"]
		}
		relEqual(t, "per campaign", got, map[string]any{"A": "1", "B": "2", "C": "1", "F": "1", "unattributed": "2"})
		total := relCall(rt, "joined_aggregate", `{`+branchPlan+`,"metrics":[{"name":"contracts","op":"count_distinct","field":"sale"}]}`)
		relEqual(t, "total", relList(total["rows"])[0]["contracts"], "7")
		// A rollup filter keeps only keys with one campaign.
		single := relCall(rt, "joined_aggregate", `{"collection":"origins","joins":[{"as":"c","collection":"calls","on":[{"local":"root.call_id","foreign":"id"}]}],
		  "rollup":{"by":["root.sale_id"],"metrics":[{"name":"n","op":"count_distinct","field":"c.campaign"}],"filter":{"field":"n","op":"eq","value":"2"}},
		  "metrics":[{"name":"sales","op":"count"}]}`)
		relEqual(t, "rollup filter", relList(single["rows"])[0]["sales"], "1")
		for label, args := range map[string]string{
			"mixed names":     `{"branches":[{"collection":"sale","select":["root.id"]},{"collection":"sale","select":["root.local_id"]}],"metrics":[{"name":"n","op":"count"}]}`,
			"mixed types":     `{"branches":[{"collection":"sale","computed":[{"name":"x","expr":{"field":"root.id"}}],"select":["x"]},{"collection":"sale","computed":[{"name":"x","expr":{"field":"root.local_id"}}],"select":["x"]}],"metrics":[{"name":"n","op":"count"}]}`,
			"no select":       `{"branches":[{"collection":"sale"}],"metrics":[{"name":"n","op":"count"}]}`,
			"root and branch": `{"collection":"sale","branches":[{"collection":"sale","select":["root.id"]}],"metrics":[{"name":"n","op":"count"}]}`,
			"rollup no by":    `{"collection":"sale","rollup":{"by":[],"metrics":[{"name":"n","op":"count"}]},"metrics":[{"name":"n","op":"count"}]}`,
			"no metrics":      `{"collection":"sale","joins":[{"as":"x","kind":"aggregate","collection":"calls","on":[{"local":"root.prospect","foreign":"prospect"}]}],"metrics":[{"name":"n","op":"count"}]}`,
			"stray metrics":   `{"collection":"sale","joins":[{"as":"x","collection":"calls","on":[{"local":"root.prospect","foreign":"prospect"}],"metrics":[{"name":"n","op":"count"}]}],"metrics":[{"name":"n","op":"count"}]}`,
		} {
			if _, e := relTry(rt, "joined_aggregate", args); e == nil {
				t.Errorf("%s was accepted", label)
			}
		}
		if _, e := relTry(rt, "joined_aggregate", `{"metrics":[{"name":"n","op":"count"}]}`); e == nil {
			t.Error("no collection and no branches was accepted")
		}
	})
}

func TestAggregateJoinCountsWithoutMultiplying(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedBranches(t, rt)
		out := relCall(rt, "joined_page", `{"collection":"sale","where":{"field":"id","op":"in","value":["2","6","11"]},"joins":[
		  {"as":"reach","collection":"calls","kind":"aggregate","on":[{"local":"root.prospect","foreign":"prospect"}],"metrics":[
		    {"name":"calls","op":"count"},{"name":"energy","op":"count","where":{"field":"energy","op":"eq","value":"1"}},{"name":"top","op":"max","field":"id"},{"name":"sum","op":"sum","field":"id","where":{"field":"campaign","op":"eq","value":"none"}}]}],
		  "orderBy":[{"field":"root.id"}],"select":["root.id","reach.calls","reach.energy","reach.top","reach.sum"]}`)
		rows := relList(out["records"])
		relEqual(t, "one row each", out["total"], "3")
		relEqual(t, "two calls", rows[0], map[string]any{"root.id": "2", "reach.calls": "2", "reach.energy": "2", "reach.top": "3", "reach.sum": nil})
		relEqual(t, "no energy", rows[2], map[string]any{"root.id": "11", "reach.calls": "1", "reach.energy": "0", "reach.top": "110", "reach.sum": nil})
	})
}

func TestBranchDistinctRemovesWhatSeveralBranchesFound(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedBranches(t, rt)
		branch := `{"collection":"sale","where":{"field":"id","op":"eq","value":"1"},"computed":[{"name":"sale","expr":{"field":"root.id"}}],"select":["sale"]}`
		args := `{"branches":[` + branch + `,` + branch + `],%s"metrics":[{"name":"n","op":"count"}]}`
		relEqual(t, "without distinct", relList(relCall(rt, "joined_aggregate", args, "")["rows"])[0]["n"], "2")
		relEqual(t, "with distinct", relList(relCall(rt, "joined_aggregate", args, `"distinct":["sale"],`)["rows"])[0]["n"], "1")
	})
}
