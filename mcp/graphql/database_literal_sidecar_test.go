package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	literalRestricted = `{"op": "gt", "args": [{"op": "length", "args": ["$scope.offers"]}, {"const": 0}]}`
	literalQGivenReal = `{"op": "neq", "args": ["$param.q", {"const": ""}]}`
	literalPhoneReal  = `{"op": "gte", "args": [{"op": "length", "args": ["$param.phone"]}, {"const": 6}]}`
	literalSDL        = `type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
type Query {
  quality(q: String = "", phone: String = "", first: Int = 100, after: String): Page!
  qualityFull(q: String = "", phone: String = "", first: Int = 100, after: String): Page!
  labels(q: String = "", first: Int = 100, after: String): Page!
  wildcard(q: String = "", first: Int = 100, after: String): Page!
}
type Subscription { liveQuality(q: String = "", phone: String = "", first: Int = 100, after: String): Page! }`
)

func literalContains(field, value string) string {
	return `{"field": "` + field + `", "op": "contains_ascii", "value": "$param.` + value + `"}`
}

// literalSearchGroup is one optional search over each piece of display context on
// the linked sales and calls, with the checks on those records kept inside. callCheck
// is what makes a linked call visible to a caller restricted to offers.
func literalSearchGroup(callCheck string) string {
	return `{"when": ` + literalQGivenReal + `, "filter": {"or": [
	    ` + literalContains("source_name", "q") + `,
	    {"exists": {"collection": "sale_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}],
	      "where": {"exists": {"collection": "sales", "on": [{"localField": "sale_ref", "foreignField": "local_id"}], "where": {"and": [
	        {"field": "centre", "op": "in", "value": "$scope.centres"},
	        {"access_when": ` + literalRestricted + `, "filter": {"field": "offer_ref", "op": "in", "value": "$scope.offers"}},
	        {"or": [` + literalContains("client_name", "q") + `, ` + literalContains("phone", "q") + `, ` + literalContains("contract", "q") + `,
	          {"exists": {"collection": "offers", "on": [{"localField": "offer_ref", "foreignField": "local_id"}], "where": ` + literalContains("nom", "q") + `}},
	          {"when": ` + literalPhoneReal + `, "filter": ` + literalContains("phone_digits", "phone") + `}]}]}}}}},
	    {"exists": {"collection": "call_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}],
	      "where": {"exists": {"collection": "calls", "on": [{"localField": "call_ref", "foreignField": "local_id"}], "where": {"and": [
	        {"field": "centre", "op": "in", "value": "$scope.centres"},
	        {"access_when": ` + literalRestricted + `, "filter": ` + callCheck + `},
	        {"or": [` + literalContains("number", "q") + `,
	          {"exists": {"collection": "prospects", "on": [{"localField": "prospect_ref", "foreignField": "local_id"}], "where": {"or": [
	            ` + literalContains("name", "q") + `, ` + literalContains("phone", "q") + `,
	            {"when": ` + literalPhoneReal + `, "filter": ` + literalContains("phone_digits", "phone") + `}]}}},
	          {"when": ` + literalPhoneReal + `, "filter": ` + literalContains("number_digits", "phone") + `}]}]}}}}}]}}`
}

const (
	literalSaleToLink = `"collection": "sale_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}]`
	literalCallToLink = `"collection": "call_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}]`
	literalLinkToSale = `"collection": "sales", "on": [{"localField": "sale_ref", "foreignField": "local_id"}]`
	literalLinkToCall = `"collection": "calls", "on": [{"localField": "call_ref", "foreignField": "local_id"}]`
	literalOnOffer    = `{"field": "offer_ref", "op": "in", "value": "$scope.offers"}`
	literalOffOffer   = `{"field": "offer_ref", "op": "not_in", "value": "$scope.offers"}`
	// literalCallAllowed: the call has a sale on a granted offer and none on another.
	literalCallAllowed = `{"and": [{"exists": {` + literalLinkToSale + `, "where": ` + literalOnOffer + `}},
	  {"not_exists": {` + literalLinkToSale + `, "where": ` + literalOffOffer + `}}]}`
	// literalFullPermission has the shape of the real reports permission, 8
	// relationships: a linked sale or call on a granted offer, and no linked sale on
	// any other. With its search that makes 16 in one request.
	literalFullPermission = `{"and": [
	  {"or": [
	    {"exists": {` + literalSaleToLink + `, "where": {"exists": {` + literalLinkToSale + `, "where": ` + literalOnOffer + `}}}},
	    {"exists": {` + literalCallToLink + `, "where": {"exists": {` + literalLinkToCall + `, "where": ` + literalCallAllowed + `}}}}]},
	  {"not_exists": {` + literalSaleToLink + `, "where": {"exists": {` + literalLinkToSale + `, "where": ` + literalOffOffer + `}}}}]}`
	// literalShortPermission is a smaller permission, which keeps a hidden sale
	// and a report that shows it on the same page.
	literalShortPermission = `{"exists": {` + literalSaleToLink + `, "where": ` + literalOnOffer + `}}`
)

// literalReportsPlan pages reports the way Quality browses them, with the smaller
// permission.
func literalReportsPlan(t *testing.T) map[string]any {
	t.Helper()
	return literalReportPage(t, literalSearchGroup(`{"exists": {`+literalLinkToSale+`, "where": `+literalOnOffer+`}}`))
}

// literalFullReportsPlan is Quality's real shape: 16 relationships in one request.
func literalFullReportsPlan(t *testing.T) map[string]any {
	t.Helper()
	return literalReportPageWith(t, literalFullPermission, literalSearchGroup(literalCallAllowed))
}

func literalReportPage(t *testing.T, search string) map[string]any {
	t.Helper()
	return literalReportPageWith(t, literalShortPermission, search)
}

// literalReportPageWith wraps a filter in the mandatory scope, which is outside
// any or: the centre, and for a caller restricted to offers, the permission.
func literalReportPageWith(t *testing.T, permission, search string) map[string]any {
	t.Helper()
	plan := decodeConfig(t, `{
	  "params": {"user": {"from": "$identity.subject", "required": true},
	    "q": {"from": "$args.q", "type": "string", "required": true},
	    "phone": {"from": "$args.phone", "type": "string", "required": true},
	    "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
	  "access": {"values": {
	    "centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
	    "offers": {"optional": true, "otherwise": {"collection": "grants", "field": "offer_ids", "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1,
	      "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": "1"}]}}}}},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 1000, "page": {"size": "$param.first", "cursor": "$param.after"},
	     "args": {"collection": "reports", "orderBy": [{"field": "id", "direction": "desc"}], "where": {"and": [
	       {"field": "centre", "op": "in", "value": "$scope.centres"},
	       {"access_when": `+literalRestricted+`, "filter": `+permission+`},
	       `+search+`]}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
	plan["database"] = "crm"
	return plan
}

func literalLabelPlan(t *testing.T, filter string) map[string]any {
	t.Helper()
	plan := decodeConfig(t, `{
	  "params": {"user": {"from": "$identity.subject", "required": true},
	    "q": {"from": "$args.q", "type": "string", "required": true},
	    "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
	  "access": {"values": {"centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 1000, "page": {"size": "$param.first", "cursor": "$param.after"},
	     "args": {"collection": "labels", "orderBy": [{"field": "id", "direction": "desc"}], "where": {"and": [
	       {"field": "centre", "op": "in", "value": "$scope.centres"}, `+filter+`]}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
	plan["database"] = "crm"
	return plan
}

// The fixture, as rows the oracle below reads directly.
var (
	literalOffersData = map[string]string{"offer-1": "Offre standard", "offer-2": "Tarif 100% vert", "offer-3": "Tarif 1000 vert", "offer-4": "Offre A_B", "offer-5": "Offre A1B"}
	literalSales      = map[string]map[string]any{
		"s1": {"client_name": "Jeanne Durand", "phone": "06 12 34 56 78", "phone_digits": "0612345678", "contract": "C-100%", "offer_ref": "offer-1", "centre": "a"},
		"s2": {"client_name": "Paul Caché", "phone": "07 00 00 00 01", "phone_digits": "0700000001", "contract": "C-200", "offer_ref": "offer-2", "centre": "a"},
		"s3": {"client_name": "Marc Autre", "phone": "07 00 00 00 02", "phone_digits": "0700000002", "contract": "C-300", "offer_ref": "offer-1", "centre": "b"},
		"s4": {"client_name": "Neutre Quatre", "phone": "", "phone_digits": "", "contract": "C-400", "offer_ref": "offer-1", "centre": "a"},
		"s5": {"client_name": "Cinq Prenom", "phone": nil, "phone_digits": nil, "contract": nil, "offer_ref": "offer-3", "centre": "a"},
		"s6": {"client_name": "Six Tiret", "phone": "", "phone_digits": "", "contract": "C-600", "offer_ref": "offer-4", "centre": "a"},
		"s7": {"client_name": "Sept", "phone": "", "phone_digits": "", "contract": "C-700", "offer_ref": "offer-5", "centre": "a"},
		"s8": {"client_name": "Jeanne Durand", "phone": "06 12 34 56 78", "phone_digits": "0612345678", "contract": "C-100%", "offer_ref": "offer-1", "centre": "b"},
	}
	literalCalls = map[string]map[string]any{
		"c1": {"number": "+33 6 99 88 77 66", "number_digits": "33699887766", "prospect_ref": "p1", "sale_ref": "s4", "centre": "a"},
		"c2": {"number": "+33 6 11 11 11 11", "number_digits": "33611111111", "prospect_ref": "p2", "sale_ref": "s2", "centre": "a"},
		"c3": {"number": "+33 6 22 22 22 22", "number_digits": "33622222222", "prospect_ref": "p3", "sale_ref": "s4", "centre": "b"},
		"c4": {"number": nil, "number_digits": nil, "prospect_ref": nil, "sale_ref": nil, "centre": "a"},
	}
	literalProspects = map[string]map[string]any{
		"p1": {"name": "Lucie Martin", "phone": "06 99 88 77 66", "phone_digits": "0699887766"},
		"p2": {"name": "Hélène Secret", "phone": "06 11 11 11 11", "phone_digits": "0611111111"},
		"p3": {"name": "Zoé Ailleurs", "phone": "06 22 22 22 22", "phone_digits": "0622222222"},
	}
	literalReports = []struct {
		id             int
		source, centre string
		sales, calls   []string
	}{
		{1, "Rapport sans contexte", "a", nil, nil},
		{2, "R2", "a", []string{"s1"}, nil},
		{3, "R3", "a", []string{"s4", "s2"}, nil}, // visible through s4; s2 is hidden from a limited caller
		{4, "R4", "a", []string{"s3"}, nil},       // s3 belongs to another centre
		{5, "R5", "a", []string{"s4"}, []string{"c1"}},
		{6, "R6", "a", []string{"s4"}, []string{"c2"}}, // c2's sale is hidden from a limited caller
		{7, "R7", "a", []string{"s4"}, []string{"c3"}}, // c3 belongs to another centre
		{8, "Tarif 100% vert", "b", []string{"s8"}, nil},
		{9, "R9", "a", []string{"s5"}, nil},
		{10, "R10", "a", []string{"s6"}, nil},
		{11, "R11", "a", []string{"s7"}, nil},
		{12, "Source 100% propre", "a", []string{"s4"}, nil},
		{13, "R13", "a", []string{"gone"}, []string{"c4"}}, // no display context at all
		{14, `O'Brien \ "quoted"`, "a", []string{"s4"}, nil},
	}
)

func literalSaleOffer(ref string) string {
	if ref == "gone" {
		return "offer-1"
	}
	return literalSales[ref]["offer_ref"].(string)
}

func literalText(value any) string {
	text, _ := value.(string)
	return text
}

// literalFold is the case rule of the SQL being replaced: ASCII letters fold,
// everything else compares exactly.
func literalFold(text string) string {
	out := []byte(text)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + 32
		}
	}
	return string(out)
}

func literalMatches(value any, q string) bool {
	return strings.Contains(literalFold(literalText(value)), literalFold(q))
}

// literalOracle works out by hand what the report should show: the same rules as
// the Quality SQL, written over the fixture with no Database or GraphQL involved.
func literalOracle(offers []string, full bool, q, phone string) string {
	allowed := func(offer any) bool {
		if len(offers) == 0 {
			return true
		}
		for _, item := range offers {
			if item == offer {
				return true
			}
		}
		return false
	}
	phoneOn := utf8.RuneCountInString(phone) >= 6
	ids := []int{}
	for _, report := range literalReports {
		if report.centre != "a" {
			continue
		}
		if len(offers) > 0 {
			visible, foreign := false, false
			for _, ref := range report.sales {
				sale, exists := literalSales[ref]
				if !full {
					visible = visible || allowed(literalSaleOffer(ref))
				} else if exists {
					visible = visible || allowed(sale["offer_ref"])
					foreign = foreign || !allowed(sale["offer_ref"])
				}
			}
			if full {
				for _, ref := range report.calls {
					sale, exists := literalSales[literalText(literalCalls[ref]["sale_ref"])]
					visible = visible || (exists && allowed(sale["offer_ref"]))
				}
			}
			if !visible || foreign {
				continue
			}
		}
		matched := q == "" || literalMatches(report.source, q)
		for _, ref := range report.sales {
			sale, exists := literalSales[ref]
			if !exists || sale["centre"] != "a" || !allowed(sale["offer_ref"]) {
				continue
			}
			matched = matched || literalMatches(sale["client_name"], q) || literalMatches(sale["phone"], q) || literalMatches(sale["contract"], q) ||
				literalMatches(literalOffersData[sale["offer_ref"].(string)], q) || (phoneOn && literalMatches(sale["phone_digits"], phone))
		}
		for _, ref := range report.calls {
			call := literalCalls[ref]
			if call["centre"] != "a" {
				continue
			}
			if len(offers) > 0 {
				sale, exists := literalSales[literalText(call["sale_ref"])]
				if !exists || !allowed(sale["offer_ref"]) {
					continue
				}
			}
			matched = matched || literalMatches(call["number"], q) || (phoneOn && literalMatches(call["number_digits"], phone))
			if prospect, exists := literalProspects[literalText(call["prospect_ref"])]; exists {
				matched = matched || literalMatches(prospect["name"], q) || literalMatches(prospect["phone"], q) || (phoneOn && literalMatches(prospect["phone_digits"], phone))
			}
		}
		if matched {
			ids = append(ids, report.id)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	parts := []string{}
	for _, id := range ids {
		parts = append(parts, fmt.Sprint(id))
	}
	return strings.Join(parts, ",")
}

func newLiteralFixture(t *testing.T, project string) *realDatabase {
	t.Helper()
	r := newRealDatabase(t, project)
	r.collection("reports", "id:integer", "source_name:text", "centre:text")
	r.collection("sale_links", "id:integer", "report_id:text", "sale_ref:text", "offer_ref:text")
	r.collection("call_links", "id:integer", "report_id:text", "call_ref:text")
	r.collection("sales", "id:integer", "local_id:text", "client_name:text", "phone:text", "phone_digits:text", "contract:text", "offer_ref:text", "centre:text")
	r.collection("offers", "id:integer", "local_id:text", "nom:text")
	r.collection("calls", "id:integer", "local_id:text", "number:text", "number_digits:text", "prospect_ref:text", "sale_ref:text", "centre:text")
	r.collection("prospects", "id:integer", "local_id:text", "name:text", "phone:text", "phone_digits:text")
	r.collection("profiles", "id:integer", "user_id:text", "centre:text")
	r.collection("grants", "id:integer", "user_id:text", "enabled:integer", "offer_ids:json")
	r.collection("labels", "id:integer", "label:text", "centre:text")
	for _, index := range [][2]string{{"sale_links", "report_id"}, {"call_links", "report_id"}, {"sales", "local_id"}, {"offers", "local_id"}, {"calls", "local_id"}, {"prospects", "local_id"}} {
		r.operator("db_index_create", map[string]any{"collection": index[0], "index": map[string]any{"name": index[0] + "_" + index[1], "fields": []any{map[string]any{"field": index[1], "direction": "asc"}}}})
	}
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	return r
}

func (r *realDatabase) seedLiteralFixture(all, limited string) {
	next := 0
	row := func(fields map[string]any, ref string) map[string]any {
		next++
		record := map[string]any{"id": fmt.Sprint(next), "local_id": ref}
		for key, value := range fields {
			record[key] = value
		}
		return record
	}
	for ref, nom := range literalOffersData {
		r.insert("offers", row(map[string]any{"nom": nom}, ref))
	}
	for ref, sale := range literalSales {
		r.insert("sales", row(sale, ref))
	}
	for ref, call := range literalCalls {
		r.insert("calls", row(call, ref))
	}
	for ref, prospect := range literalProspects {
		r.insert("prospects", row(prospect, ref))
	}
	for _, report := range literalReports {
		r.insert("reports", map[string]any{"id": fmt.Sprint(report.id), "source_name": report.source, "centre": report.centre})
		for _, sale := range report.sales {
			next++
			r.insert("sale_links", map[string]any{"id": fmt.Sprint(next), "report_id": fmt.Sprint(report.id), "sale_ref": sale, "offer_ref": literalSaleOffer(sale)})
		}
		for _, call := range report.calls {
			next++
			r.insert("call_links", map[string]any{"id": fmt.Sprint(next), "report_id": fmt.Sprint(report.id), "call_ref": call})
		}
	}
	r.insert("profiles", map[string]any{"id": "1", "user_id": all, "centre": "a"}, map[string]any{"id": "2", "user_id": limited, "centre": "a"})
	r.insert("grants", map[string]any{"id": "1", "user_id": limited, "enabled": "1", "offer_ids": []any{"offer-1"}})
	r.insert("labels",
		map[string]any{"id": "1", "label": "Tarif 100% vert", "centre": "a"},
		map[string]any{"id": "2", "label": "Tarif 1000 vert", "centre": "a"},
		map[string]any{"id": "3", "label": "Offre A_B", "centre": "a"},
		map[string]any{"id": "4", "label": "Offre A1B", "centre": "a"},
		map[string]any{"id": "5", "label": nil, "centre": "a"},
		map[string]any{"id": "6", "label": "Tarif 100% vert", "centre": "b"},
		map[string]any{"id": "7", "label": "Énergie", "centre": "a"},
		map[string]any{"id": "8", "label": "", "centre": "a"})
}

func publishLiteral(r *realDatabase, t *testing.T, quality map[string]any) string {
	return r.publish(literalSDL, map[string]map[string]any{
		"Query.quality":            {"operation": "database_pipeline", "config": quality},
		"Query.qualityFull":        {"operation": "database_pipeline", "config": literalFullReportsPlan(t)},
		"Query.labels":             {"operation": "database_pipeline", "config": literalLabelPlan(t, literalContains("label", "q"))},
		"Query.wildcard":           {"operation": "database_pipeline", "config": literalLabelPlan(t, `{"fields": ["label"], "op": "like_contains_ascii", "value": "$param.q"}`)},
		"Subscription.liveQuality": {"operation": "database_pipeline", "config": quality},
	})
}

// TestRealDatabaseLiteralSearch runs contains_ascii through publication, the
// handoff's acceptance cases, a Quality-shaped report against an oracle, paging,
// and live updates, on a real Database that has the operator.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseLiteralSearch -v
func TestRealDatabaseLiteralSearch(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_DATABASE_LITERAL") == "refused" {
		t.Skip("this Database has no contains_ascii")
	}
	r := newLiteralFixture(t, "graphql-database-literal-search")
	const everyOffer, oneOffer = "41", "42"
	r.permissions[everyOffer], r.permissions[oneOffer] = []string{}, []string{}
	r.seedLiteralFixture(everyOffer, oneOffer)

	// Publication checks the operator on the collection that owns the field, at
	// every depth, so a bad field refuses the release rather than the first search.
	sales := func(where string) string {
		return `{"exists": {"collection": "sale_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}],
		  "where": {"exists": {"collection": "sales", "on": [{"localField": "sale_ref", "foreignField": "local_id"}], "where": ` + where + `}}}}`
	}
	guarded := func(filter string) string { return `{"when": ` + literalQGivenReal + `, "filter": ` + filter + `}` }
	for name, c := range map[string]struct{ search, want string }{
		"own integer field":     {guarded(literalContains("id", "q")), "contains_ascii requires a text field"},
		"own unknown field":     {guarded(literalContains("nope", "q")), "nope"},
		"own field in an or":    {guarded(`{"or": [` + literalContains("source_name", "q") + `, ` + literalContains("id", "q") + `]}`), "contains_ascii requires a text field"},
		"own phone branch":      {guarded(`{"or": [` + literalContains("source_name", "q") + `, {"when": ` + literalPhoneReal + `, "filter": ` + literalContains("id", "phone") + `}]}`), "contains_ascii requires a text field"},
		"related integer field": {guarded(sales(literalContains("id", "q"))), "contains_ascii requires a text field"},
		"related unknown field": {guarded(sales(literalContains("nope", "q"))), "nope"},
		"a report-only field":   {guarded(sales(literalContains("source_name", "q"))), "source_name"},
		"related phone branch":  {guarded(sales(`{"or": [` + literalContains("phone", "q") + `, {"when": ` + literalPhoneReal + `, "filter": ` + literalContains("id", "phone") + `}]}`)), "contains_ascii requires a text field"},
		"nested integer field": {guarded(sales(`{"exists": {"collection": "offers", "on": [{"localField": "offer_ref", "foreignField": "local_id"}], "where": ` +
			literalContains("id", "q") + `}}`)), "contains_ascii requires a text field"},
	} {
		t.Run("publish refuses: "+name, func(t *testing.T) {
			failure := publishLiteral(r, t, literalReportPage(t, c.search))
			if failure == "" || !strings.Contains(failure, c.want) {
				t.Errorf("got %q, want it to contain %q", failure, c.want)
			}
		})
	}
	for name, sdl := range map[string]string{
		"a number argument": strings.Replace(literalSDL, `quality(q: String = ""`, `quality(q: Int = 0`, 1),
		"a missing default": strings.Replace(literalSDL, `quality(q: String = ""`, `quality(q: String`, 1),
		"a phone number": strings.Replace(literalSDL, `phone: String = "", first: Int = 100, after: String): Page!
  labels`, `phone: Int = 0, first: Int = 100, after: String): Page!
  labels`, 1),
	} {
		t.Run("publish refuses: "+name, func(t *testing.T) {
			if failure := r.publish(sdl, map[string]map[string]any{"Query.quality": {"operation": "database_pipeline", "config": literalReportsPlan(t)}}); failure == "" {
				t.Error("was published")
			}
		})
	}
	if failure := publishLiteral(r, t, literalReportsPlan(t)); failure != "" {
		t.Fatal(failure)
	}

	ask := func(user, field string, variables map[string]any) (string, string, string, string) {
		t.Helper()
		signature, arguments := "$q: String, $first: Int, $after: String", "q: $q, first: $first, after: $after"
		if strings.HasPrefix(field, "quality") {
			signature, arguments = signature+", $phone: String", arguments+", phone: $phone"
		}
		out := r.queryAs(user, fmt.Sprintf(`query(%s) { %s(%s) { rows { id } total next } }`, signature, field, arguments), variables)
		data, _ := out.Data[field].(map[string]any)
		ids := []string{}
		rows, _ := data["rows"].([]any)
		for _, row := range rows {
			ids = append(ids, fmt.Sprint(row.(map[string]any)["id"]))
		}
		next := ""
		if data["next"] != nil {
			next = fmt.Sprint(data["next"])
		}
		return strings.Join(ids, ","), fmt.Sprint(data["total"]), next, out.code()
	}
	variables := func(q, phone string) map[string]any {
		return map[string]any{"q": q, "phone": phone, "first": 100}
	}
	count := func(ids string) string {
		if ids == "" {
			return "0"
		}
		return fmt.Sprint(strings.Count(ids, ",") + 1)
	}

	// The handoff's acceptance cases, on one field.
	for _, c := range []struct{ q, want string }{
		{"100%", "1"}, {"1000", "2"}, {"A_B", "3"}, {"a_b", "3"}, {"A1B", "4"}, {"%", "1"}, {"_", "3"},
		{"énergie", ""}, {"Énergie", "7"}, {"ÉNERGIE", "7"}, {"nothing", ""}, {"", "8,7,5,4,3,2,1"},
		{"  ", ""}, {`\`, ""},
	} {
		t.Run(fmt.Sprintf("acceptance %q", c.q), func(t *testing.T) {
			ids, total, _, code := ask(oneOffer, "labels", variables(c.q, ""))
			if code != "" || ids != c.want || total != count(c.want) {
				t.Errorf("got %q total %s (%s), want %q", ids, total, code, c.want)
			}
		})
	}
	// The wildcard search keeps its contract.
	for _, c := range []struct{ q, want string }{{"100%", "2,1"}, {"A_B", "4,3"}, {"tarif", "2,1"}} {
		if ids, total, _, code := ask(oneOffer, "wildcard", variables(c.q, "")); code != "" || ids != c.want || total != count(c.want) {
			t.Errorf("wildcard %q: got %q total %s (%s), want %q", c.q, ids, total, code, c.want)
		}
	}

	// Quality-shaped: what each kind of caller may find, written out by hand.
	for _, c := range []struct {
		name, q, phone string
		every, one     string
	}{
		{"blank: every authorised report, with or without context", "", "", "14,13,12,11,10,9,7,6,5,4,3,2,1", "14,13,12,7,6,5,4,3,2"},
		{"blank ignores the phone", "", "0612345678", "14,13,12,11,10,9,7,6,5,4,3,2,1", "14,13,12,7,6,5,4,3,2"},
		{"own field and a hidden offer name", "100%", "", "12,3,2", "12,2"},
		{"percent is literal", "%", "", "12,3,2", "12,2"},
		{"digits are not the percent", "1000", "", "9", ""},
		{"underscore is literal", "A_B", "", "10", ""},
		{"not the underscore", "A1B", "", "11", ""},
		{"a sale's client", "Jeanne", "", "2", "2"},
		{"a sale's client, folded", "jeanne DURAND", "", "2", "2"},
		{"a sale's phone as stored", "06 12", "", "2", "2"},
		{"a sale's contract", "C-400", "", "14,12,7,6,5,3", "14,12,7,6,5,3"},
		{"a hidden sale", "Paul", "", "3", ""},
		{"a sale in another centre", "Marc Autre", "", "", ""},
		{"a call's number", "+33 6 99", "", "5", "5"},
		{"a prospect", "Lucie", "", "5", "5"},
		{"a prospect's phone as stored", "06 99 88", "", "5", "5"},
		{"a call hidden by its sale", "Hélène", "", "6", ""},
		{"a call in another centre", "Zoé", "", "", ""},
		{"accents compare exactly", "caché", "", "3", ""},
		{"non-ASCII does not fold", "CACHÉ", "", "", ""},
		{"spaces are kept", "  Jeanne", "", "", ""},
		{"a phone on a sale", "zzz", "0612345678", "2", "2"},
		{"too short to search phones", "zzz", "06123", "", ""},
		{"six is long enough", "zzz", "061234", "2", "2"},
		{"a phone on a hidden sale", "zzz", "0700000001", "3", ""},
		{"a phone on a call", "zzz", "33699887766", "5", "5"},
		{"a phone on a prospect", "zzz", "0699887766", "5", "5"},
		{"a phone on a hidden prospect", "zzz", "0611111111", "6", ""},
		{"a phone in another centre", "zzz", "0622222222", "", ""},
		{"either q or the phone", "Lucie", "0612345678", "5,2", "5,2"},
		{"a backslash", `\`, "", "14", "14"},
		{"a quote", `"quoted"`, "", "14", "14"},
		{"an apostrophe", "O'B", "", "14", "14"},
	} {
		t.Run("quality: "+c.name, func(t *testing.T) {
			for user, want := range map[string]string{everyOffer: c.every, oneOffer: c.one} {
				ids, total, _, code := ask(user, "quality", variables(c.q, c.phone))
				if code != "" || ids != want || total != count(want) {
					t.Errorf("user %s: got %q total %s (%s), want %q", user, ids, total, code, want)
				}
			}
		})
	}

	// The real permission's shape, 16 relationships in one request. A report with a
	// linked sale on another offer is out altogether, and a hidden call still can't
	// bring a visible report back.
	for _, c := range []struct {
		name, q, phone string
		every, one     string
	}{
		{"blank", "", "", "14,13,12,11,10,9,7,6,5,4,3,2,1", "14,12,7,6,5,4,2"},
		{"own field and a hidden offer name", "100%", "", "12,3,2", "12,2"},
		{"a mixed report stays out", "C-400", "", "14,12,7,6,5,3", "14,12,7,6,5"},
		{"a hidden sale", "Paul", "", "3", ""},
		{"a sale in another centre", "Marc Autre", "", "", ""},
		{"a visible call", "Lucie", "", "5", "5"},
		{"a call hidden by its sale", "Hélène", "", "6", ""},
		{"a phone on a sale", "zzz", "0612345678", "2", "2"},
		{"a phone on a hidden prospect", "zzz", "0611111111", "6", ""},
	} {
		t.Run("full permission: "+c.name, func(t *testing.T) {
			for user, want := range map[string]string{everyOffer: c.every, oneOffer: c.one} {
				ids, total, _, code := ask(user, "qualityFull", variables(c.q, c.phone))
				if code != "" || ids != want || total != count(want) {
					t.Errorf("user %s: got %q total %s (%s), want %q", user, ids, total, code, want)
				}
			}
		})
	}

	// Everything again against the oracle, with terms taken from the data itself.
	terms := []string{"", " ", "  ", "%", "_", `\`, "100", "100%", "1000", "tarif", "vert", "A_B", "a1b", "offre", "standard", "c-", "C-100", "C-1", "Jeanne", "Durand", "Paul", "Marc", "Neutre", "Cinq", "Prenom", "Six", "Sept", "+33", "33 6", "Lucie", "Hélène", "Zoé", "Martin", "06", "07 00", "R1", "R", "source", "rapport", "contexte", "O'Brien", `"`, "é", "É", "zzz"}
	phones := []string{"", "12345", "123456", "0612345678", "0700000001", "0700000002", "33611111111", "0611111111", "0622222222", "000000"}
	queries := 0
	for _, q := range terms {
		for _, phone := range phones {
			if phone != "" && phone != "0700000001" && phone != "0611111111" && q != "zzz" && q != "" && q != "Jeanne" {
				continue
			}
			for user, offers := range map[string][]string{everyOffer: nil, oneOffer: {"offer-1"}} {
				for field, full := range map[string]bool{"quality": false, "qualityFull": true} {
					want := literalOracle(offers, full, q, phone)
					ids, total, _, code := ask(user, field, variables(q, phone))
					queries++
					if code != "" || ids != want || total != count(want) {
						t.Errorf("%s user %s q %q phone %q: got %q total %s (%s), oracle %q", field, user, q, phone, ids, total, code, want)
					}
				}
			}
		}
	}
	t.Logf("%d queries matched the oracle", queries)

	// The total stays the whole match on every page, and a cursor belongs to its search.
	page := map[string]any{"q": "C-400", "phone": "", "first": 2}
	first, total, cursor, code := ask(oneOffer, "quality", page)
	if first != "14,12" || total != "6" || cursor == "" || code != "" {
		t.Fatalf("first page: %q total %s next %q (%s)", first, total, cursor, code)
	}
	page["after"] = cursor
	if ids, total, next, code := ask(oneOffer, "quality", page); ids != "7,6" || total != "6" || next == "" || code != "" {
		t.Errorf("second page: %q total %s next %q (%s)", ids, total, next, code)
	}
	for name, change := range map[string]map[string]any{
		"another search": {"q": "C-40", "phone": "", "first": 2, "after": cursor},
		"another phone":  {"q": "C-400", "phone": "123456", "first": 2, "after": cursor},
	} {
		if ids, _, _, code := ask(oneOffer, "quality", change); code == "" || ids != "" {
			t.Errorf("cursor under %s: %q (%s)", name, ids, code)
		}
	}
	if ids, _, _, code := ask(everyOffer, "quality", page); code == "" || ids != "" {
		t.Errorf("cursor under another policy: %q (%s)", ids, code)
	}

	// Live: edits to a searched record and permission changes reach rows and total
	// at once. Database's change capture exists on AptevaDB only.
	if adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER"); adapter != "" && adapter != "aptevadb" {
		return
	}
	r.operator("db_realtime_configure", map[string]any{"enabled": true})
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(r.graph.URL(), "http")+"/public/realtime/default", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ack wsMessage
	if conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + pipelineToken(oneOffer)}}) != nil || conn.ReadJSON(&ack) != nil || ack.Type != "connection_ack" {
		t.Fatalf("realtime init: %+v", ack)
	}
	if err := conn.WriteJSON(map[string]any{"id": "live", "type": "subscribe", "payload": map[string]any{"query": `subscription { liveQuality(q: "Jeanne") { rows { id } total } }`}}); err != nil {
		t.Fatal(err)
	}
	latest := "?"
	await := func(step, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for latest != want {
			_ = conn.SetReadDeadline(deadline)
			var message wsMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("%s: have %q, want %q: %v", step, latest, want, err)
			}
			if message.Type == "error" {
				t.Fatalf("%s: %s", step, message.Payload)
			}
			if message.Type != "next" {
				continue
			}
			var payload struct {
				Data struct {
					LiveQuality struct {
						Rows []struct {
							ID string `json:"id"`
						} `json:"rows"`
						Total int `json:"total"`
					} `json:"liveQuality"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("%s: payload %s", step, message.Payload)
			}
			ids := []string{}
			for _, row := range payload.Data.LiveQuality.Rows {
				ids = append(ids, row.ID)
			}
			if payload.Data.LiveQuality.Total != len(ids) {
				t.Fatalf("%s: total %d for rows %v", step, payload.Data.LiveQuality.Total, ids)
			}
			latest = strings.Join(ids, ",")
		}
	}
	setSale := func(ref string, set map[string]any) {
		t.Helper()
		r.operator("db_update", map[string]any{"collection": "sales", "where": map[string]any{"field": "local_id", "op": "eq", "value": ref}, "set": set, "maxAffected": 1})
	}
	grant := func(offers ...any) {
		t.Helper()
		r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "id", "op": "eq", "value": "1"}, "set": map[string]any{"offer_ids": offers}, "maxAffected": 1})
	}
	await("initial", "2")
	setSale("s1", map[string]any{"client_name": "Anonyme"})
	await("the record stops matching", "")
	setSale("s1", map[string]any{"client_name": "Jeanne Durand"})
	await("restored", "2")
	setSale("s4", map[string]any{"client_name": "Jeanne Neutre"})
	await("another linked record starts matching", "14,12,7,6,5,3,2")
	setSale("s4", map[string]any{"client_name": "Neutre Quatre"})
	await("back to one", "2")
	setSale("s1", map[string]any{"centre": "b"})
	await("the matching record leaves the centre", "")
	setSale("s1", map[string]any{"centre": "a"})
	await("it returns", "2")
	setSale("s2", map[string]any{"client_name": "Jeanne Cachée"})
	grant("offer-1", "offer-2")
	await("a grant reveals the hidden sale", "3,2")
	grant("offer-1")
	await("revoking it hides the sale again", "2")
	grant("offer-3")
	await("the permission moves to another offer", "")
	grant("offer-1")
	await("restored once more", "2")
	r.operator("db_delete", map[string]any{"collection": "profiles", "where": map[string]any{"field": "user_id", "op": "eq", "value": oneOffer}, "maxAffected": 1})
	await("the centre is revoked", "")
}

// TestRealDatabaseLiteralSearchRefusedAtPublish runs on a Database without the
// operator: a plan using it is refused at publish, on its own field and through a
// relationship, and everything else keeps working.
//
//	GRAPHQL_TEST_DATABASE_LITERAL=refused GRAPHQL_TEST_DATABASE_DIR=<older database>/mcp/database GOWORK=off go test -run TestRealDatabaseLiteralSearchRefusedAtPublish -v
func TestRealDatabaseLiteralSearchRefusedAtPublish(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_DATABASE_LITERAL") != "refused" {
		t.Skip("set GRAPHQL_TEST_DATABASE_LITERAL=refused on a Database without contains_ascii")
	}
	r := newLiteralFixture(t, "graphql-database-literal-refused")
	const everyOffer, oneOffer = "41", "42"
	r.permissions[everyOffer], r.permissions[oneOffer] = []string{}, []string{}
	r.seedLiteralFixture(everyOffer, oneOffer)
	guarded := func(filter string) string { return `{"when": ` + literalQGivenReal + `, "filter": ` + filter + `}` }
	for name, search := range map[string]string{
		"own field": guarded(literalContains("source_name", "q")),
		"related": guarded(`{"exists": {"collection": "sale_links", "on": [{"localField": "id", "foreignField": "report_id", "comparison": "text"}],
		  "where": ` + literalContains("sale_ref", "q") + `}}`),
	} {
		t.Run(name, func(t *testing.T) {
			failure := r.publish(literalSDL, map[string]map[string]any{"Query.quality": {"operation": "database_pipeline", "config": literalReportPage(t, search)}})
			if !strings.Contains(failure, "contains_ascii") {
				t.Errorf("got %q", failure)
			}
		})
	}
	withoutSearch := literalReportPage(t, `{"field": "centre", "op": "is_not_null"}`)
	if failure := r.publish(literalSDL, map[string]map[string]any{
		"Query.quality":            {"operation": "database_pipeline", "config": withoutSearch},
		"Query.qualityFull":        {"operation": "database_pipeline", "config": withoutSearch},
		"Query.labels":             {"operation": "database_pipeline", "config": literalLabelPlan(t, `{"fields": ["label"], "op": "like_contains_ascii", "value": "$param.q"}`)},
		"Query.wildcard":           {"operation": "database_pipeline", "config": literalLabelPlan(t, `{"fields": ["label"], "op": "like_contains_ascii", "value": "$param.q"}`)},
		"Subscription.liveQuality": {"operation": "database_pipeline", "config": withoutSearch},
	}); failure != "" {
		t.Errorf("a plan without it: %s", failure)
	}
}

// TestRealDatabaseRelationshipLimit publishes and runs plans around the limit of the
// Database in use, which GRAPHQL_TEST_DATABASE_CLAUSES names when it isn't 24. A plan
// past an older Database's limit is refused at publish, not on the first request.
//
//	GRAPHQL_TEST_DATABASE_CLAUSES=8 GRAPHQL_TEST_DATABASE_DIR=<older database>/mcp/database GOWORK=off go test -run TestRealDatabaseRelationshipLimit -v
func TestRealDatabaseRelationshipLimit(t *testing.T) {
	limit := 24
	if os.Getenv("GRAPHQL_TEST_DATABASE_CLAUSES") == "8" {
		limit = 8
	}
	r := newRealDatabase(t, "graphql-database-relationship-limit")
	r.collection("reports", "id:integer", "centre:text")
	r.collection("links", "id:integer", "report_id:integer", "kind:text")
	r.collection("profiles", "id:integer", "user_id:text", "centre:text")
	r.operator("db_index_create", map[string]any{"collection": "links", "index": map[string]any{"name": "links_report", "fields": []any{map[string]any{"field": "report_id", "direction": "asc"}}}})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	const reader = "7"
	r.permissions[reader] = []string{}
	r.insert("reports", map[string]any{"id": "1", "centre": "a"}, map[string]any{"id": "2", "centre": "a"})
	r.insert("links", map[string]any{"id": "1", "report_id": "1", "kind": "x"})
	r.insert("profiles", map[string]any{"id": "1", "user_id": reader, "centre": "a"})
	plan := func(count int) map[string]any {
		parts := []string{`{"field": "centre", "op": "in", "value": "$scope.centres"}`}
		for range count {
			parts = append(parts, `{"exists": {"collection": "links", "on": [{"localField": "id", "foreignField": "report_id"}], "where": {"field": "kind", "op": "eq", "value": "x"}}}`)
		}
		out := decodeConfig(t, `{"params": {"user": {"from": "$identity.subject", "required": true}},
		  "access": {"values": {"centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
		  "steps": [{"id": "rows", "op": "find", "max_rows": 100, "args": {"collection": "reports", "orderBy": [{"field": "id"}], "where": {"and": [`+strings.Join(parts, ",")+`]}}},
		    {"id": "total", "op": "count", "count_of": "rows"}],
		  "result": {"rows": "$steps.rows", "total": "$steps.total"}}`)
		out["database"] = "crm"
		return out
	}
	const sdl = `type Row { id: ID! } type Page { rows: [Row!]! total: Int! } type Query { page: Page! }`
	publish := func(count int) string {
		return r.publish(sdl, map[string]map[string]any{"Query.page": {"operation": "database_pipeline", "config": plan(count)}})
	}
	if failure := publish(limit); failure != "" {
		t.Fatalf("%d relationships: %s", limit, failure)
	}
	out := r.queryAs(reader, `{ page { rows { id } total } }`, nil)
	if page, _ := out.Data["page"].(map[string]any); out.code() != "" || fmt.Sprint(page["total"]) != "1" {
		t.Errorf("%d relationships ran: %v (%s)", limit, out.Data, out.code())
	}
	failure := publish(limit + 1)
	want := fmt.Sprintf("at most %d", limit)
	if limit == 24 {
		want = "at most 24"
	}
	if failure == "" || !strings.Contains(failure, want) {
		t.Errorf("%d relationships: got %q, want it to contain %q", limit+1, failure, want)
	}
}
