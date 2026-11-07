package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// callsPage has the shape of a real activity page: a key and a sort value that
// fall back across several fields, and one selected field.
const callsPage = `{"collection":"calls",%s"computed":[
  {"name":"interaction_key","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.interaction_id"}},{"nullIfEmpty":{"field":"root.local_id"}},{"field":"root.crm_row_id"}]}},
  {"name":"sort_at","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.status_at"}},{"nullIfEmpty":{"field":"root.started_at"}},{"nullIfEmpty":{"field":"root.ended_at"}},{"field":"root.created_at"}]}},
  {"name":"inflight","type":"integer","expr":{"if":{"field":"root.status","op":"in","value":["transcribing","evaluating"]},"then":{"const":"1"},"else":{"const":"0"}}}],
  "group":{"by":["%s"],"orderBy":[{"field":"inflight","direction":"desc","agg":"max"},{"field":"sort_at","direction":"desc","agg":"max"},{"field":"interaction_key","direction":"desc"}]},
  "orderBy":[{"field":"root.id","direction":"desc"}],"select":["root.id"],"limit":25%s}`

const callsFlatPage = `{"collection":"calls",%s"computed":[
  {"name":"interaction_key","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.interaction_id"}},{"nullIfEmpty":{"field":"root.local_id"}},{"field":"root.crm_row_id"}]}},
  {"name":"sort_at","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.status_at"}},{"nullIfEmpty":{"field":"root.started_at"}},{"nullIfEmpty":{"field":"root.ended_at"}},{"field":"root.created_at"}]}}],
  "orderBy":[{"field":"sort_at","direction":"desc"},{"field":"root.id","direction":"desc"}],"select":["root.id"],"limit":25%s}`

type seededCall struct {
	id, key, sortAt string
	inflight        bool
}

// seedCalls writes rows as wide as the real ones. legs says how many rows share an
// interaction; a few interactions are in flight so that order isn't only by date.
func seedCalls(t *testing.T, rt relT, interactions, legs int) []seededCall {
	t.Helper()
	relCollection(rt, "calls", "id", "id integer,interaction_id text,local_id text,crm_row_id text,status text,status_at text,started_at text,ended_at text,created_at text,centre text", "centre")
	seeded := make([]seededCall, 0, interactions*legs)
	batch := make([]map[string]any, 0, MaxRecords)
	flush := func() {
		if len(batch) > 0 {
			relInsert(rt, "calls", batch...)
			batch = batch[:0]
		}
	}
	id := 0
	for i := 0; i < interactions; i++ {
		key := fmt.Sprintf("interaction-%08d-0123456789abcdefghijkl", i)
		for leg := 0; leg < legs; leg++ {
			id++
			at := fmt.Sprintf("2026-10-%02dT%02d:%02d:%02d.000Z", 1+id%28, id%24, id%60, (id/7)%60)
			status := "completed"
			if i%97 == 3 {
				status = "transcribing"
			}
			batch = append(batch, map[string]any{
				"id": strconv.Itoa(id), "interaction_id": key, "local_id": "local-" + key, "crm_row_id": "crm-" + key,
				"status": status, "status_at": at, "started_at": at, "ended_at": at, "created_at": at,
				"centre": fmt.Sprintf("centre_%06d", i%40),
			})
			seeded = append(seeded, seededCall{id: strconv.Itoa(id), key: key, sortAt: at, inflight: status != "completed"})
			if len(batch) == MaxRecords {
				flush()
			}
		}
	}
	flush()
	return seeded
}

type expectedCallGroup struct {
	key      string
	inflight bool
	sortAt   string
	ids      []string
}

// expectCallGroups is the page order worked out from the seeded rows.
func expectCallGroups(calls []seededCall) []expectedCallGroup {
	byKey := map[string]*expectedCallGroup{}
	groups := []*expectedCallGroup{}
	for _, c := range calls {
		g := byKey[c.key]
		if g == nil {
			g = &expectedCallGroup{key: c.key}
			byKey[c.key] = g
			groups = append(groups, g)
		}
		g.inflight = g.inflight || c.inflight
		if c.sortAt > g.sortAt {
			g.sortAt = c.sortAt
		}
		g.ids = append(g.ids, c.id)
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.inflight != b.inflight {
			return a.inflight
		}
		if a.sortAt != b.sortAt {
			return a.sortAt > b.sortAt
		}
		return a.key > b.key
	})
	out := make([]expectedCallGroup, len(groups))
	for i, g := range groups {
		ids := append([]string{}, g.ids...)
		sort.Slice(ids, func(i, j int) bool {
			a, _ := strconv.Atoi(ids[i])
			b, _ := strconv.Atoi(ids[j])
			return a > b
		})
		g.ids = ids
		out[i] = *g
	}
	return out
}

func checkCallGroups(t *testing.T, label string, out map[string]any, want []expectedCallGroup, total int) {
	t.Helper()
	relEqual(t, label+" total", out["total"], strconv.Itoa(total))
	groups := relList(out["groups"])
	if len(groups) != len(want) {
		t.Fatalf("%s: %d groups, want %d", label, len(groups), len(want))
	}
	for i, g := range groups {
		key, _ := g["key"].(map[string]any)
		relEqual(t, label+" key", key["interaction_key"], want[i].key)
		relEqual(t, label+" legs", relStrings(relList(g["rows"]), "root.id"), want[i].ids)
	}
}

func withRetained(t *testing.T, limit int) {
	t.Helper()
	old := relationalRetained
	relationalRetained = limit
	t.Cleanup(func() { relationalRetained = old })
}

// The fixture's rows cost about 2 KiB each to keep, so 6 MiB of rows can't fit in
// this limit. Paging only needs each item's sort values and identity, which do.
func TestJoinedPageKeepsOnlyWhatPagingNeeds(t *testing.T) {
	const interactions = 3000
	relEach(t, func(t *testing.T, rt relT) {
		calls := seedCalls(t, rt, interactions, 1)
		want := expectCallGroups(calls)
		withRetained(t, 3<<20)
		first := relCall(rt, "joined_page", callsPage, "", "interaction_key", "")
		checkCallGroups(t, "first page", first, want[:25], interactions)
		relEqual(t, "more after the first page", first["hasMore"], true)
		second := relCall(rt, "joined_page", callsPage, "", "interaction_key", `,"offset":25`)
		checkCallGroups(t, "second page", second, want[25:50], interactions)
		next := relCall(rt, "joined_page", callsPage, "", "interaction_key", fmt.Sprintf(`,"cursor":%q`, first["nextCursor"]))
		checkCallGroups(t, "cursor page", next, want[25:50], interactions)
		last := relCall(rt, "joined_page", callsPage, "", "interaction_key", fmt.Sprintf(`,"offset":%d`, interactions-10))
		checkCallGroups(t, "last page", last, want[interactions-10:], interactions)
		relEqual(t, "nothing after the last page", last["hasMore"], false)
		past := relCall(rt, "joined_page", callsPage, "", "interaction_key", fmt.Sprintf(`,"offset":%d`, interactions+5))
		checkCallGroups(t, "past the end", past, nil, interactions)

		// Rows as items hold the same promise.
		flat := relCall(rt, "joined_page", callsFlatPage, "", "")
		relEqual(t, "flat total", flat["total"], strconv.Itoa(interactions))
		byDate := append([]seededCall{}, calls...)
		sort.Slice(byDate, func(i, j int) bool {
			if byDate[i].sortAt != byDate[j].sortAt {
				return byDate[i].sortAt > byDate[j].sortAt
			}
			a, _ := strconv.Atoi(byDate[i].id)
			b, _ := strconv.Atoi(byDate[j].id)
			return a > b
		})
		ids := make([]string, 25)
		for i := range ids {
			ids[i] = byDate[i].id
		}
		relEqual(t, "flat page", relStrings(relList(flat["records"]), "root.id"), ids)
		flatNext := relCall(rt, "joined_page", callsFlatPage, "", fmt.Sprintf(`,"cursor":%q`, flat["nextCursor"]))
		for i := range ids {
			ids[i] = byDate[25+i].id
		}
		relEqual(t, "flat cursor page", relStrings(relList(flatNext["records"]), "root.id"), ids)

		// A distinct that spans records decides its winners in the first pass, so it
		// doesn't have to keep whole rows either.
		spanning := relCall(rt, "joined_page", callsPage, `"distinct":["root.interaction_id"],`, "interaction_key", "")
		checkCallGroups(t, "distinct across records", spanning, want[:25], interactions)
		// Pick has to compare later rows with its current winners, so it keeps their
		// order values. That's far less than the 12 MiB it used to keep here.
		withRetained(t, 5<<20)
		picked := relCall(rt, "joined_page", callsPage, `"pick":{"by":["root.interaction_id"],"orderBy":[{"field":"root.id","direction":"desc"}]},`, "interaction_key", "")
		checkCallGroups(t, "pick", picked, want[:25], interactions)
	})
}

// Winners are decided over the whole input, so a page has to show the rows that won,
// not whichever rows its own groups would produce alone.
func TestJoinedPageShowsTheRowsThatWon(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id integer,interaction text,at text,sale text,rank integer")
		relInsert(rt, "legs",
			// i0 sorts last, so it's outside a one-item page, and its legs come first.
			map[string]any{"id": "1", "interaction": "i0", "at": "2026-01-01", "sale": "shared", "rank": "1"},
			map[string]any{"id": "2", "interaction": "i2", "at": "2026-03-01", "sale": "shared", "rank": "9"},
			map[string]any{"id": "3", "interaction": "i2", "at": "2026-03-02", "sale": "own", "rank": "2"},
			map[string]any{"id": "4", "interaction": "i2", "at": "2026-03-03", "sale": "own", "rank": "5"},
			map[string]any{"id": "5", "interaction": "i1", "at": "2026-02-01", "sale": "other", "rank": "1"})
		page := func(extra string) map[string]any {
			return relCall(rt, "joined_page", `{"collection":"legs",%s"group":{"by":["root.interaction"],"orderBy":[{"field":"root.at","direction":"desc","agg":"max"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":1}`, extra)
		}
		// Leg 2 lost its sale to leg 1, in a group this page doesn't show.
		out := page(`"distinct":["root.sale"],`)
		relEqual(t, "distinct total", out["total"], "3")
		relEqual(t, "a row that lost in another group stays out", relStrings(relList(relList(out["groups"])[0]["rows"]), "root.id"), []string{"3"})
		// Leg 3 beats leg 4 for "own", and leg 1 takes "shared" from another group.
		out = page(`"pick":{"by":["root.sale"],"orderBy":[{"field":"root.rank"}]},`)
		relEqual(t, "pick total", out["total"], "3")
		relEqual(t, "pick keeps the winner only", relStrings(relList(relList(out["groups"])[0]["rows"]), "root.id"), []string{"3"})
		// With rank above 2 required, legs 1 and 3 can't win, so legs 2 and 4 do.
		relEqual(t, "pick with where", relStrings(relList(relList(page(`"pick":{"by":["root.sale"],"where":{"field":"root.rank","op":"gt","value":"2"},"orderBy":[{"field":"root.rank"}]},`)["groups"])[0]["rows"]), "root.id"), []string{"2", "4"})
	})
}

func refusedForMemory(t *testing.T, label string, e error) {
	t.Helper()
	var failure *Error
	if !errors.As(e, &failure) || failure.Code != "resource_limit" {
		t.Errorf("%s: got %v, want resource_limit", label, e)
	}
}

func TestJoinedPageStillRefusesWhatCannotFit(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedCalls(t, rt, 3000, 1)
		// Too many groups to page, even with nothing kept of their rows.
		withRetained(t, 256<<10)
		_, e := relTry(rt, "joined_page", callsPage, "", "interaction_key", "")
		refusedForMemory(t, "too many groups", e)
		// One group is cheap to page, but the page is every row. All of them have to fit.
		withRetained(t, 2<<20)
		oneGroup := `{"collection":"calls","computed":[{"name":"everything","expr":{"const":"all"}}],"group":{"by":["everything"],"orderBy":[{"field":"everything"}]},"orderBy":[{"field":"root.id"}],"select":["root.interaction_id","root.local_id","root.crm_row_id","root.status","root.status_at","root.started_at","root.ended_at","root.created_at"],"limit":1}`
		_, e = relTry(rt, "joined_page", oneGroup)
		refusedForMemory(t, "one group too large for its page", e)
		// Rows as items cost their keeping too.
		withRetained(t, 256<<10)
		_, e = relTry(rt, "joined_page", callsFlatPage, "", "")
		refusedForMemory(t, "too many rows", e)
		withRetained(t, 2<<20)
		// The same limit allows a page that does fit.
		small := `{"collection":"calls","where":{"field":"centre","op":"eq","value":"centre_000001"},"computed":[{"name":"everything","expr":{"const":"all"}}],"group":{"by":["everything"],"orderBy":[{"field":"everything"}]},"orderBy":[{"field":"root.id"}],"select":["root.interaction_id"],"limit":1}`
		if out, e := relTry(rt, "joined_page", small); e != nil || out["total"] != "1" {
			t.Fatalf("a small group: %v %v", out, e)
		}
	})
}

// What the first pass keeps counts even for items outside the page.
func TestJoinedPageCountsItemsOutsideThePage(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text,body text")
		rows := []map[string]any{{"id": "first", "interaction": "a", "body": "a"}, {"id": "wide-first", "interaction": "z", "body": "a"}, {"id": "wide-next", "interaction": "z", "body": strings.Repeat("z", 200<<10)}}
		relInsert(rt, "legs", rows...)
		many := []map[string]any{}
		for i := 0; i < 3000; i++ {
			many = append(many, map[string]any{"id": fmt.Sprintf("%04d", i), "interaction": "y", "body": "b"})
			if len(many) == MaxRecords {
				relInsert(rt, "legs", many...)
				many = many[:0]
			}
		}
		// Group a sorts first, so the page never shows z, whose largest sort value grows
		// past the limit, or y and its thousands of legs.
		byBody := `{"collection":"legs","where":{"field":"interaction","op":"in","value":%s},"group":{"by":["root.interaction"],"orderBy":[{"field":"root.body","agg":"max"},{"field":"root.interaction"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":1}`
		withRetained(t, 100<<10)
		out := relCall(rt, "joined_page", byBody, `["a"]`)
		relEqual(t, "the narrow group alone fits", relStrings(relList(relList(out["groups"])[0]["rows"]), "root.id"), []string{"first"})
		_, e := relTry(rt, "joined_page", byBody, `["a","z"]`)
		refusedForMemory(t, "a sort value that grew", e)
		_, e = relTry(rt, "joined_page", byBody, `["a","y"]`)
		refusedForMemory(t, "legs outside the page", e)
	})
}

// A field nothing uses isn't read for the page, so it doesn't count.
func TestJoinedPageIgnoresFieldsItDoesNotUse(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text,body text")
		body := strings.Repeat("x", 100<<10)
		for i := 0; i < 30; i += 5 {
			rows := []map[string]any{}
			for j := i; j < i+5; j++ {
				rows = append(rows, map[string]any{"id": fmt.Sprintf("%02d", j), "interaction": fmt.Sprintf("i%d", j%3), "body": body})
			}
			relInsert(rt, "legs", rows...)
		}
		withRetained(t, 1<<20)
		out := relCall(rt, "joined_page", `{"collection":"legs","group":{"by":["root.interaction"],"orderBy":[{"field":"root.interaction"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":3}`)
		relEqual(t, "total", out["total"], "3")
		relEqual(t, "legs", len(relList(relList(out["groups"])[0]["rows"])), 10)
	})
}

// Once the page is chosen, only the page counts. Every item on one page needs
// 2,066,712 bytes on this fixture, its rows and which rows they are. These limits sit
// just either side, so holding on to the first pass's items, a distinct's keys or pick's
// winners while rows are read would be refused, and so would not counting the page.
func TestJoinedPageReleasesWhatItNoLongerNeeds(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		const interactions = 1000
		calls := seedCalls(t, rt, interactions, 1)
		want := expectCallGroups(calls)
		withRetained(t, 2_120_000)
		out := relCall(rt, "joined_page", callsPage, "", "interaction_key", `,"limit":1000`)
		checkCallGroups(t, "every item on one page", out, want, interactions)
		out = relCall(rt, "joined_page", callsPage, `"distinct":["root.interaction_id"],`, "interaction_key", `,"limit":1000`)
		checkCallGroups(t, "distinct with every item on one page", out, want, interactions)
		out = relCall(rt, "joined_page", callsPage, `"pick":{"by":["root.interaction_id"],"orderBy":[{"field":"root.id","direction":"desc"}]},`, "interaction_key", `,"limit":1000`)
		checkCallGroups(t, "pick with every item on one page", out, want, interactions)
		withRetained(t, 2_030_000)
		_, e := relTry(rt, "joined_page", callsPage, "", "interaction_key", `,"limit":1000`)
		refusedForMemory(t, "just under what the page needs", e)
		// Each better row replaces pick's winner, so only the 40 winners ever count.
		withRetained(t, 200<<10)
		picked := relCall(rt, "joined_page", callsFlatPage, `"pick":{"by":["root.centre"],"orderBy":[{"field":"root.id","direction":"desc"}]},`, "")
		relEqual(t, "one winner per centre", picked["total"], "40")
	})
}

// These can't happen on one snapshot. If they ever did, a page must fail rather than
// come back short, so they are tested by asking for what isn't there.
func TestJoinedPageRefusesRowsItCannotFindAgain(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text")
		relInsert(rt, "legs", map[string]any{"id": "1", "interaction": "a"})
		ctx := context.Background()
		d, release, e := rt.m.acquireDatabase(ctx, "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		read := func(leg relLeg) ([]Record, error) {
			item := &relItem{legs: []relLeg{leg}}
			e := d.backend.transaction(ctx, false, func(tx transaction) error {
				c, e := tx.collection("legs")
				if e != nil {
					return e
				}
				budget := &relBudget{ctx: ctx}
				p, e := newRelChain(tx, rootView(c, "root"), []string{"root"}, budget, nil, nil, nil, nil)
				if e != nil {
					return e
				}
				p.finish()
				return relReadLegs(tx, c, "root", p, budget, []*relItem{item}, nil)
			})
			return item.rows, e
		}
		if rows, e := read(relLeg{root: `["1"]`}); e != nil || len(rows) != 1 {
			t.Fatalf("the row that's there: %v %v", rows, e)
		}
		var failure *Error
		if _, e := read(relLeg{root: `["gone"]`}); !errors.As(e, &failure) || failure.Code != "internal" {
			t.Errorf("a record that's gone: %v", e)
		}
		if _, e := read(relLeg{root: `["1"]`, joined: "other"}); !errors.As(e, &failure) || failure.Code != "internal" {
			t.Errorf("a row the record no longer produces: %v", e)
		}
	})
}

// The second pass does work, so it spends the work budget.
func TestJoinedPageSpendsWorkOnBothPasses(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text")
		rows := []map[string]any{}
		for i := 0; i < 100; i++ {
			rows = append(rows, map[string]any{"id": fmt.Sprintf("%03d", i), "interaction": fmt.Sprintf("i%03d", i)})
		}
		relInsert(rt, "legs", rows...)
		page := `{"collection":"legs","orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":25}`
		old := relationalWork
		defer func() { relationalWork = old }()
		relationalWork = 125
		if out, e := relTry(rt, "joined_page", page); e != nil || out["total"] != "100" {
			t.Fatalf("100 records and 25 rereads: %v %v", out, e)
		}
		relationalWork = 124
		var failure *Error
		if _, e := relTry(rt, "joined_page", page); !errors.As(e, &failure) || failure.Code != "query_too_expensive" {
			t.Errorf("over budget by one reread: %v", e)
		}
	})
}

// A page that would exceed the result size stops early and resumes by cursor.
func TestJoinedPageCutAtTheResultSizeResumes(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text,body text")
		body := strings.Repeat("x", 400<<10)
		rows := []map[string]any{}
		for i := 0; i < 15; i++ {
			rows = append(rows, map[string]any{"id": fmt.Sprintf("%02d", i), "interaction": fmt.Sprintf("i%d", i/3), "body": body})
		}
		for i := 0; i < len(rows); i += 5 {
			relInsert(rt, "legs", rows[i:i+5]...)
		}
		page := `{"collection":"legs","group":{"by":["root.interaction"],"orderBy":[{"field":"root.interaction"}]},"orderBy":[{"field":"root.id"}],"select":["root.id","root.body"],"limit":10%s}`
		seen := []string{}
		extra := ""
		pages := 0
		for {
			out := relCall(rt, "joined_page", page, extra)
			relEqual(t, "total", out["total"], "5")
			for _, g := range relList(out["groups"]) {
				seen = append(seen, relStrings(relList(g["rows"]), "root.id")...)
			}
			pages++
			if out["hasMore"] != true {
				break
			}
			extra = fmt.Sprintf(`,"cursor":%q`, out["nextCursor"])
		}
		if pages < 2 {
			t.Errorf("the result fit in %d page", pages)
		}
		want := []string{}
		for i := 0; i < 15; i++ {
			want = append(want, fmt.Sprintf("%02d", i))
		}
		relEqual(t, "every leg, once, in order", seen, want)
		_, e := relTry(rt, "joined_page", `{"collection":"legs","computed":[{"name":"all","expr":{"const":"x"}}],"group":{"by":["all"],"orderBy":[{"field":"all"}]},"orderBy":[{"field":"root.id"}],"select":["root.body"]}`)
		var failure *Error
		if !errors.As(e, &failure) || failure.Code != "resource_limit" || !strings.Contains(e.Error(), "4 MiB") {
			t.Errorf("one group over the result size: %v", e)
		}
	})
}

// Composite and non-text primary keys, and text a key encoding has to escape, must
// come back through the second pass unchanged.
func TestJoinedPageRereadsEveryKindOfPrimaryKey(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCall(rt, "collection_create", `{"collection":"keyed","primaryKey":["k_text","k_int","k_bool","k_num","k_at"],"fields":[
		  {"name":"k_text","type":"text"},{"name":"k_int","type":"integer"},{"name":"k_bool","type":"boolean"},{"name":"k_num","type":"number"},{"name":"k_at","type":"datetime"},
		  {"name":"payload","type":"json","nullable":true},{"name":"label","type":"text","nullable":true}]}`)
		texts := []string{`quote"inside`, `back\slash`, "é<&>", " spaced ", "line\nbreak", "0012", "tab\there", " "}
		rows := []map[string]any{}
		for i, text := range texts {
			rows = append(rows, map[string]any{"k_text": text, "k_int": strconv.Itoa(i - 3), "k_bool": i%2 == 0, "k_num": float64(i) / 4, "k_at": fmt.Sprintf("2026-10-0%dT00:00:00.000001Z", i+1), "payload": map[string]any{"i": i}, "label": fmt.Sprintf("row-%d", i)})
		}
		relInsert(rt, "keyed", rows...)
		out := relCall(rt, "joined_page", `{"collection":"keyed","orderBy":[{"field":"root.label"}],"select":["root.label","root.k_text","root.k_int","root.k_bool","root.k_num","root.k_at","root.payload"],"limit":1000}`)
		records := relList(out["records"])
		if len(records) != len(texts) {
			t.Fatalf("%d records: %v", len(records), out)
		}
		for i, text := range texts {
			relEqual(t, "text key", records[i]["root.k_text"], text)
			relEqual(t, "integer key", records[i]["root.k_int"], strconv.Itoa(i-3))
			relEqual(t, "boolean key", records[i]["root.k_bool"], i%2 == 0)
			relEqual(t, "datetime key", records[i]["root.k_at"], fmt.Sprintf("2026-10-0%dT00:00:00.000001Z", i+1))
			if fmt.Sprint(records[i]["root.k_num"]) != strconv.FormatFloat(float64(i)/4, 'g', -1, 64) {
				t.Errorf("number key %d: %v", i, records[i]["root.k_num"])
			}
			if records[i]["root.payload"] == nil {
				t.Errorf("payload %d was not returned", i)
			}
		}
	})
}

// A writer keeps every interaction at exactly two legs, atomically, while pages read.
// A second pass outside the first pass's snapshot would find rows gone or moved.
func TestJoinedPageReadsOneSnapshot(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "legs", "id", "id text,interaction text,at text", "interaction")
		const interactions = 40
		rows := []map[string]any{}
		for i := 0; i < interactions; i++ {
			for leg := 0; leg < 2; leg++ {
				rows = append(rows, map[string]any{"id": fmt.Sprintf("%d-%d-0", i, leg), "interaction": fmt.Sprintf("i%02d", i), "at": "2026-10-01"})
			}
		}
		relInsert(rt, "legs", rows...)
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		var writes atomic.Int64
		writerDone := make(chan error, 1)
		go func() {
			generation := 0
			for ctx.Err() == nil {
				generation++
				i := generation % interactions
				replace := []Operation{}
				for leg := 0; leg < 2; leg++ {
					var remove, insert Request
					decode(marshal(map[string]any{"collection": "legs", "key": map[string]any{"id": fmt.Sprintf("%d-%d-%d", i, leg, lastGeneration(generation, interactions))}}), &remove)
					decode(marshal(map[string]any{"collection": "legs", "records": []map[string]any{{"id": fmt.Sprintf("%d-%d-%d", i, leg, generation), "interaction": fmt.Sprintf("i%02d", i), "at": fmt.Sprintf("2026-10-%02d", 1+generation%28)}}}), &insert)
					replace = append(replace, Operation{"delete", remove}, Operation{"insert", insert})
				}
				if _, e := rt.m.Execute(ctx, "p", "batch", Request{Database: "d", Operations: replace}); e != nil {
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
		page := `{"collection":"legs","group":{"by":["root.interaction"],"orderBy":[{"field":"root.at","direction":"desc","agg":"max"}]},"orderBy":[{"field":"root.id"}],"select":["root.id"],"limit":5%s}`
		for round := 0; round < 60; round++ {
			var failure *Error
			out, e := relTry(rt, "joined_page", page, fmt.Sprintf(`,"offset":%d`, round%interactions))
			if errors.As(e, &failure) && (failure.Code == "busy" || failure.Code == "timeout") {
				continue
			}
			if e != nil {
				t.Fatalf("round %d: %v", round, e)
			}
			relEqual(t, "total", out["total"], strconv.Itoa(interactions))
			for _, g := range relList(out["groups"]) {
				if n := len(relList(g["rows"])); n != 2 {
					t.Fatalf("round %d: interaction with %d legs: %v", round, n, g)
				}
			}
		}
		stop()
		if e := <-writerDone; e != nil {
			t.Fatalf("writer: %v", e)
		}
		if writes.Load() == 0 {
			t.Fatal("nothing was written during the reads")
		}
	})
}

// lastGeneration is the generation that last wrote this interaction.
func lastGeneration(generation, interactions int) int {
	if last := generation - interactions; last > 0 {
		return last
	}
	return 0
}

// The size of the October activity that failed on 0.12.0, at the real limit.
func TestJoinedPageAtActivityScale(t *testing.T) {
	if os.Getenv("DATABASE_PAGE_SCALE") == "" {
		t.Skip("set DATABASE_PAGE_SCALE=1; this writes 73,048 records per adapter")
	}
	const interactions = 73048
	relEach(t, func(t *testing.T, rt relT) {
		seedStart := time.Now()
		calls := seedCalls(t, rt, interactions, 1)
		want := expectCallGroups(calls)
		t.Logf("seeded in %s", time.Since(seedStart).Round(time.Millisecond))
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		first := relCall(rt, "joined_page", callsPage, "", "interaction_key", "")
		t.Logf("offset 0 in %s", time.Since(start).Round(time.Millisecond))
		runtime.ReadMemStats(&after)
		t.Logf("allocated %d MiB during the read", (after.TotalAlloc-before.TotalAlloc)>>20)
		checkCallGroups(t, "first page", first, want[:25], interactions)
		start = time.Now()
		second := relCall(rt, "joined_page", callsPage, "", "interaction_key", `,"offset":25`)
		t.Logf("offset 25 in %s", time.Since(start).Round(time.Millisecond))
		checkCallGroups(t, "second page", second, want[25:50], interactions)
		// This page's first pass costs about 34 MiB of the 128 MiB. Rising far above
		// that would bring the refusal back, and the guard must still apply at this size.
		withRetained(t, 48<<20)
		if _, e := relTry(rt, "joined_page", callsPage, "", "interaction_key", ""); e != nil {
			t.Errorf("48 MiB should be enough: %v", e)
		}
		withRetained(t, 24<<20)
		_, e := relTry(rt, "joined_page", callsPage, "", "interaction_key", "")
		refusedForMemory(t, "24 MiB", e)
	})
}

// corpusFixture has interactions of several legs, ties in sort values, nulls, rows a
// left join doesn't match, and joins that multiply rows.
func corpusFixture(t *testing.T, rt relT) {
	t.Helper()
	relCollection(rt, "legs", "id", "id integer,interaction text,centre text,started text,status text,sale_ref text,rank integer", "centre")
	relCollection(rt, "sales", "id", "id integer,local_id text,centre text,amount integer,tag text", "local_id")
	relCollection(rt, "aliases", "id", "id text,leg_id integer,sale_id integer", "leg_id")
	relCollection(rt, "blocked", "id", "id text,interaction text,centre text", "interaction")
	relCall(rt, "index_create", `{"collection":"sales","index":{"name":"by_id_centre","fields":[{"field":"id","direction":"asc"},{"field":"centre","direction":"asc"}]}}`)
	random := rand.New(rand.NewSource(20261105))
	statuses := []any{"completed", "transcribing", nil, "", "evaluating"}
	legs := []map[string]any{}
	id := 0
	for i := 0; i < 24; i++ {
		for leg := random.Intn(5); leg >= 0; leg-- {
			id++
			ref := []any{fmt.Sprintf("S-%d", random.Intn(12)), strconv.Itoa(random.Intn(12)), "none", nil}[random.Intn(4)]
			legs = append(legs, map[string]any{"id": strconv.Itoa(id), "interaction": fmt.Sprintf("i%02d", i), "centre": []string{"a", "a", "b"}[random.Intn(3)],
				"started": fmt.Sprintf("2026-10-%02d", 1+random.Intn(4)), "status": statuses[random.Intn(len(statuses))], "sale_ref": ref, "rank": strconv.Itoa(random.Intn(4))})
		}
	}
	relInsert(rt, "legs", legs...)
	sales := []map[string]any{}
	for i := 0; i < 12; i++ {
		sales = append(sales, map[string]any{"id": strconv.Itoa(i), "local_id": fmt.Sprintf("S-%d", i), "centre": []string{"a", "b"}[i%2], "amount": strconv.Itoa(100 * (i % 5)), "tag": []any{"x", "y", nil}[i%3]})
	}
	relInsert(rt, "sales", sales...)
	aliases := []map[string]any{}
	for leg := 1; leg <= id; leg++ {
		for n := random.Intn(4); n > 0; n-- {
			aliases = append(aliases, map[string]any{"id": fmt.Sprintf("a%d-%d", leg, n), "leg_id": strconv.Itoa(leg), "sale_id": strconv.Itoa(random.Intn(12))})
		}
	}
	relInsert(rt, "aliases", aliases...)
	relInsert(rt, "blocked", map[string]any{"id": "b1", "interaction": "i03", "centre": "a"}, map[string]any{"id": "b2", "interaction": "i07", "centre": "b"})
}

type corpusCase struct{ label, body string }

// corpusCases crosses the things that decide which rows a page shows.
func corpusCases() []corpusCase {
	computed := `"computed":[{"name":"state","expr":{"coalesce":[{"nullIfEmpty":{"field":"root.status"}},{"const":"none"}]}}]`
	joins := []struct {
		name, joins string
		fields      []string
	}{
		{"nojoin", ``, nil},
		{"leftsale", `"joins":[{"as":"blocked","collection":"blocked","kind":"anti","on":[{"local":"root.interaction","foreign":"interaction"},{"local":"root.centre","foreign":"centre"}]},
		  {"as":"sale","collection":"sales","kind":"left","anyOf":[{"on":[{"local":"root.sale_ref","foreign":"local_id"},{"local":"root.centre","foreign":"centre"}]},{"on":[{"local":"root.sale_ref","foreign":"id","comparison":"numeric"},{"local":"root.centre","foreign":"centre"}]}]}],`, []string{"sale.id", "sale.amount", "sale.tag"}},
		{"chain", `"joins":[{"as":"alias","collection":"aliases","on":[{"local":"root.id","foreign":"leg_id"}]},{"as":"sale","collection":"sales","on":[{"local":"alias.sale_id","foreign":"id"}]}],`, []string{"alias.id", "sale.id", "sale.amount", "sale.tag"}},
		{"semiagg", `"joins":[{"as":"has","collection":"aliases","kind":"semi","on":[{"local":"root.id","foreign":"leg_id"}]},{"as":"reach","collection":"aliases","kind":"aggregate","on":[{"local":"root.id","foreign":"leg_id"}],"metrics":[{"name":"n","op":"count"},{"name":"top","op":"max","field":"sale_id"}]}],`, []string{"reach.n", "reach.top"}},
	}
	out := []corpusCase{}
	for _, j := range joins {
		distincts := [][2]string{{"none", ``}, {"root", `"distinct":["root"],`}, {"field", `"distinct":["root.interaction"],`}, {"status", `"distinct":["root.status"],`}}
		picks := [][2]string{{"none", ``}, {"best", `"pick":{"by":["root.interaction"],"orderBy":[{"field":"root.rank","direction":"desc"},{"field":"root.id"}]},`},
			{"where", `"pick":{"by":["root.centre","root.interaction"],"where":{"field":"root.rank","op":"gt","value":"0"},"orderBy":[{"field":"root.started"}]},`}}
		groups := [][2]string{{"rows", ``},
			{"interaction", `"group":{"by":["root.interaction"],"orderBy":[{"field":"root.started","direction":"desc","agg":"max"},{"field":"root.interaction"}]},`},
			{"min", `"group":{"by":["root.centre","root.interaction"],"orderBy":[{"field":"root.rank","agg":"min"}]},`},
			{"computed", `"group":{"by":["state"],"orderBy":[{"field":"state","direction":"desc"}]},`}}
		switch j.name {
		case "leftsale", "chain":
			distincts = append(distincts, [2]string{"joined", `"distinct":["sale"],`}, [2]string{"pair", `"distinct":["root","sale"],`}, [2]string{"joinedfield", `"distinct":["sale.tag"],`})
			picks = append(picks, [2]string{"joined", `"pick":{"by":["sale.id"],"orderBy":[{"field":"root.started","direction":"desc"}]},`})
			groups = append(groups, [2]string{"joinedkey", `"group":{"by":["sale.tag"],"orderBy":[{"field":"sale.amount","direction":"desc","agg":"max"}]},`})
		}
		orders := [][2]string{{"id", `"orderBy":[{"field":"root.id","direction":"desc"}]`}, {"started", `"orderBy":[{"field":"root.started"}]`}}
		narrows := [][2]string{{"all", ``}, {"where", `"where":{"field":"centre","op":"eq","value":"a"},`}, {"filter", `"filter":{"field":"root.rank","op":"gt","value":"0"},`},
			{"exists", `"where":{"exists":{"collection":"aliases","on":[{"localField":"id","foreignField":"leg_id"}]}},`}}
		switch j.name {
		case "leftsale", "chain":
			narrows = append(narrows, [2]string{"joined", `"filter":{"or":[{"field":"sale.amount","op":"gt","value":"100"},{"field":"sale.tag","op":"eq","value":"x"}]},`})
		}
		selects := []string{"root.id", "root.status"}
		selects = append(selects, j.fields...)
		sel := `"select":["` + strings.Join(selects, `","`) + `"]`
		for _, d := range distincts {
			for _, pk := range picks {
				for _, g := range groups {
					for _, o := range orders {
						for _, narrow := range narrows {
							body := `{"collection":"legs",` + narrow[1] + j.joins + computed + `,` + d[1] + pk[1] + g[1] + o[1] + `,` + sel + `}`
							out = append(out, corpusCase{strings.Join([]string{j.name, d[0], pk[0], g[0], o[0], narrow[0]}, "/"), body})
						}
					}
				}
			}
		}
	}
	// Every field selected, which is what a request with no select gets.
	out = append(out, corpusCase{"everything/rows", `{"collection":"legs",` + computed + `,"orderBy":[{"field":"root.id"}]}`},
		corpusCase{"everything/groups", `{"collection":"legs",` + computed + `,"group":{"by":["root.interaction"],"orderBy":[{"field":"root.interaction"}]},"orderBy":[{"field":"root.id"}]}`},
		// An alias other than the default.
		corpusCase{"alias/rows", `{"collection":"legs","as":"call","joins":[{"as":"alias","collection":"aliases","on":[{"local":"call.id","foreign":"leg_id"}]}],"orderBy":[{"field":"call.started"}],"select":["call.id","alias.id"]}`},
		corpusCase{"alias/groups", `{"collection":"legs","as":"call","joins":[{"as":"alias","collection":"aliases","kind":"left","on":[{"local":"call.id","foreign":"leg_id"}]}],"distinct":["call","alias"],"pick":{"by":["call.interaction","alias.sale_id"],"orderBy":[{"field":"call.rank"}]},"group":{"by":["call.interaction"],"orderBy":[{"field":"call.rank","direction":"desc","agg":"max"}]},"orderBy":[{"field":"call.id"}],"select":["call.id","alias.id"]}`})
	return out
}

// corpusRun walks one request every way a client can, and digests all it was given.
func corpusRun(rt relT, body string) string {
	digest := sha256.New()
	page := func(extra string) (map[string]any, bool) {
		args := strings.TrimSuffix(body, "}") + extra + "}"
		var r Request
		if e := decode([]byte(args), &r); e != nil {
			rt.t.Fatalf("decode %s: %v", args, e)
		}
		r.Database = "d"
		v, e := rt.m.Execute(context.Background(), "p", "joined_page", r)
		if e != nil {
			fmt.Fprintf(digest, "error %v\n", e)
			return nil, false
		}
		out := map[string]any{}
		if e := decode(marshal(v), &out); e != nil {
			rt.t.Fatal(e)
		}
		cursor, _ := out["nextCursor"].(string)
		out["nextCursor"] = cursor != ""
		digest.Write(marshal(out))
		digest.Write([]byte{'\n'})
		if cursor != "" {
			out["cursor"] = cursor
		}
		return out, true
	}
	full, ok := page(`,"limit":1000`)
	if !ok {
		return hex.EncodeToString(digest.Sum(nil))[:16]
	}
	total, _ := strconv.Atoi(full["total"].(string))
	size := max(total/4, 1)
	for offset := 0; offset <= total+size; offset += size {
		if _, ok := page(fmt.Sprintf(`,"limit":%d,"offset":%d`, size, offset)); !ok {
			break
		}
	}
	extra := fmt.Sprintf(`,"limit":%d`, size)
	for steps := 0; steps <= total+1; steps++ {
		out, ok := page(extra)
		if !ok {
			break
		}
		cursor, more := out["cursor"].(string)
		if !more {
			break
		}
		extra = fmt.Sprintf(`,"limit":%d,"cursor":%q`, size, cursor)
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

const corpusGolden = "testdata/joined_page_0120.txt"

// Compares every way of paging the corpus with what Database 0.12.0 answered, which
// is recorded in testdata. JOINED_PAGE_GOLDEN=<path> writes the answers instead, so
// the record can be made again on that release's own code. A normal run takes one case
// in twenty, each on one adapter, because CI runs under the race detector;
// JOINED_PAGE_FULL=1 runs them all on all adapters, as a release should.
func TestJoinedPageMatchesTheRelease(t *testing.T) {
	cases := corpusCases()
	want := map[string]string{}
	writing := os.Getenv("JOINED_PAGE_GOLDEN")
	if writing == "" {
		file, e := os.Open(corpusGolden)
		if e != nil {
			t.Fatal(e)
		}
		defer file.Close()
		lines := bufio.NewScanner(file)
		for lines.Scan() {
			label, sum, ok := strings.Cut(lines.Text(), " ")
			if !ok {
				t.Fatalf("bad line %q", lines.Text())
			}
			want[label] = sum
		}
		if e := lines.Err(); e != nil {
			t.Fatal(e)
		}
		if len(want) != len(cases) {
			t.Fatalf("record has %d cases, this test has %d", len(want), len(cases))
		}
	}
	full := writing != "" || os.Getenv("JOINED_PAGE_FULL") != ""
	got := map[string]map[string]string{}
	for index, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		got[adapter] = map[string]string{}
		t.Run(adapter, func(t *testing.T) {
			rt := relOpen(t, adapter)
			corpusFixture(t, rt)
			wrong := 0
			for i, c := range cases {
				if !full && (i%20 != 0 || (i/20)%3 != index) {
					continue
				}
				sum := corpusRun(rt, c.body)
				got[adapter][c.label] = sum
				if writing == "" && sum != want[c.label] {
					if wrong++; wrong <= 20 {
						t.Errorf("%s: %s, recorded %s\n%s", c.label, sum, want[c.label], c.body)
					}
				}
			}
			if wrong > 0 {
				t.Errorf("%d of %d cases differ from the release", wrong, len(cases))
			}
		})
	}
	if writing != "" {
		var b strings.Builder
		for _, c := range cases {
			first := got["sqlite"][c.label]
			for _, adapter := range []string{"pebble", "aptevadb"} {
				if got[adapter][c.label] != first {
					t.Errorf("%s: %s answers differently from sqlite", c.label, adapter)
				}
			}
			fmt.Fprintf(&b, "%s %s\n", c.label, first)
		}
		if e := os.WriteFile(writing, []byte(b.String()), 0o644); e != nil {
			t.Fatal(e)
		}
	}
}
