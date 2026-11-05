package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// fakeDatabase implements the request and response contract of Database's read
// batch. It records exactly what GraphQL sends, and answers from respond.
type fakeDatabase struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu      sync.Mutex
	batches [][]map[string]any
	respond func(ctx context.Context, op string, args map[string]any) (any, error)
	// refuse decides what Database would refuse, before anything is read.
	refuse func(op string, args map[string]any) error
	// afterRead runs once a batch's answers are final, like a read that has its
	// snapshot and hasn't returned yet. It ignores cancellation on purpose.
	afterRead func(operations []map[string]any)
	commits   chan []string
	seq       int
	watches   []map[string]any
}

func (f *fakeDatabase) CallAppResult(app, tool string, input map[string]any, out any) error {
	return f.CallAppResultContext(context.Background(), app, tool, input, out)
}

func (f *fakeDatabase) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if app != "database" {
		return fmt.Errorf("unexpected app %s", app)
	}
	var result any
	switch tool {
	case "db_changes":
		after, _ := input["after"].(string)
		f.mu.Lock()
		f.watches = append(f.watches, input)
		f.mu.Unlock()
		if after == "" {
			result = map[string]any{"events": []any{}, "cursor": "c0"}
			break
		}
		select {
		case changed := <-f.commits:
			f.mu.Lock()
			f.seq++
			cursor := fmt.Sprintf("c%d", f.seq)
			f.mu.Unlock()
			names := make([]any, len(changed))
			for i, name := range changed {
				names[i] = name
			}
			result = map[string]any{"events": []any{map[string]any{"type": "invalidate", "collections": names}}, "cursor": cursor}
		case <-ctx.Done():
			return ctx.Err()
		}
	case "db_batch":
		raw, _ := json.Marshal(input["operations"])
		var operations []map[string]any
		if err := json.Unmarshal(raw, &operations); err != nil {
			return err
		}
		f.mu.Lock()
		f.batches = append(f.batches, operations)
		f.mu.Unlock()
		answers := make([]any, len(operations))
		for i, operation := range operations {
			op, _ := operation["op"].(string)
			args, _ := operation["args"].(map[string]any)
			if f.refuse != nil {
				if err := f.refuse(op, args); err != nil {
					return err
				}
			}
			answer, err := f.respond(ctx, op, args)
			if err != nil {
				return err
			}
			answers[i] = answer
		}
		if f.afterRead != nil {
			f.afterRead(operations)
		}
		result = map[string]any{"results": answers}
	default:
		return fmt.Errorf("unexpected tool %s", tool)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func (f *fakeDatabase) sent() [][]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]map[string]any{}, f.batches...)
}

// lastJoined returns the args of the most recent joined read sent.
func (f *fakeDatabase) lastJoined(t *testing.T) map[string]any {
	t.Helper()
	batches := f.sent()
	for i := len(batches) - 1; i >= 0; i-- {
		for _, operation := range batches[i] {
			if op, _ := operation["op"].(string); isJoinedOp(op) {
				return operation["args"].(map[string]any)
			}
		}
	}
	t.Fatal("no joined read was sent")
	return nil
}

func (f *fakeDatabase) forget() {
	f.mu.Lock()
	f.batches = nil
	f.mu.Unlock()
}

func viewer(subject string, permissions ...string) context.Context {
	return context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: subject, Permissions: permissions, Expires: time.Now().Add(time.Hour)})
}

func sameJSON(t *testing.T, what string, got any, want string) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var left, right any
	if err := json.Unmarshal(encoded, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		t.Fatalf("%s: bad expectation: %v", what, err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Errorf("%s:\n got %s\nwant %s", what, encoded, want)
	}
}

const joinedParams = `
    "user": {"from": "$identity.subject", "type": "string", "required": true},
    "perms": {"from": "$identity.permissions", "type": "string_list"},
    "limit": {"from": "$args.limit", "type": "number"},
    "offset": {"from": "$args.offset", "type": "number"},
    "cursor": {"from": "$args.cursor", "type": "string"},
    "centre": {"from": "$args.centreId", "type": "string"},
    "q": {"from": "$args.q", "type": "string"}`

// Who may see which centres and offers: all-centres viewers read every centre, the
// rest their own profile's. Offer grants are optional, and only offer-scoped viewers
// have any.
const joinedAccess = `"access": {"values": {
    "centres": {"unrestricted_if_permission": "all_centres", "optional": true,
      "default": {"collection": "centres", "field": "local_id"},
      "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "offers": {"type": "exact_string_list", "optional": true,
      "otherwise": {"collection": "grants", "field": "offer_ids", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}
  }}`

const joinedOffersWhen = `{"op": "gt", "args": [{"op": "length", "args": ["$scope.offers"]}, {"const": 0}]}`

// The report's own rows: visible centres, narrowed by an optional client centre.
const joinedReportWhere = `{"and": [
    {"field": "centre_id", "op": "in", "value": "$scope.centres"},
    {"when": {"op": "neq", "args": ["$param.centre", {"const": ""}]}, "filter": {"field": "centre_id", "op": "eq", "value": "$param.centre"}}
  ]}`

func joinedContext(callPolicy string) string {
	return `{"as": "display_context", "limit": 40, "sources": [
      {"name": "sales",
       "joins": [
         {"as": "l", "collection": "sale_links", "anyOf": [{"on": [{"local": "root.id", "foreign": "report_id", "comparison": "text"}]}]},
         {"as": "s", "collection": "sales", "on": [{"local": "l.sale_ref", "foreign": "local_id"}],
          "where": {"and": [{"access_when": ` + joinedOffersWhen + `, "filter": {"field": "offer_id", "op": "in", "value": "$scope.offers"}}]}},
         {"as": "o", "collection": "offers", "kind": "left", "on": [{"local": "s.offer_id", "foreign": "local_id"}]}],
       "computed": [{"name": "client_name", "expr": {"trim": {"field": "s.client"}}}, {"name": "phone", "expr": {"field": "s.phone"}}],
       "orderBy": [{"field": "s.id", "direction": "asc"}],
       "select": ["client_name", "phone"]},
      {"name": "calls",
       "joins": [
         {"as": "l", "collection": "call_links", "on": [{"local": "root.id", "foreign": "report_id", "comparison": "text"}]},
         {"as": "c", "collection": "calls", "on": [{"local": "l.call_ref", "foreign": "local_id"}], "where": ` + callPolicy + `},
         {"as": "p", "collection": "prospects", "kind": "left", "on": [{"local": "c.prospect_id", "foreign": "id", "comparison": "numeric"}]}],
       "computed": [{"name": "client_name", "expr": {"trim": {"field": "p.name"}}}, {"name": "phone", "expr": {"field": "p.phone"}}],
       "select": ["client_name", "phone"]}
    ]}`
}

const joinedSearch = `{"and": [{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"or": [
    {"field": "client_name", "op": "contains_ascii", "value": "$param.q"},
    {"field": "phone", "op": "contains_ascii", "value": "$param.q"}]}}]}`

const joinedListCalls = `{"and": [{"access_when": ` + joinedOffersWhen + `, "filter": {"field": "offer_mix", "op": "eq", "value": "single"}}]}`
const joinedSummaryCalls = `{"field": "state", "op": "ne", "value": "deleted"}`

func qualityListPlan() string {
	return `{"database": "crm", "params": {` + joinedParams + `}, ` + joinedAccess + `, "steps": [
    {"id": "page", "op": "related_read", "max_rows": 100, "args": {
      "collection": "reports", "where": ` + joinedReportWhere + `,
      "select": ["id", "local_id", "score_total"],
      "orderBy": [{"field": "modified_time", "direction": "desc"}, {"field": "id", "direction": "desc"}],
      "count": true, "relation": ` + joinedContext(joinedListCalls) + `, "match": ` + joinedSearch + `},
     "page": {"size": "$param.limit", "offset": "$param.offset", "cursor": "$param.cursor"},
     "shape": {"total_as": "total_count", "json_text": ["display_context"], "numbers": ["score_total"]}}
  ], "result": "$steps.page"}`
}

func qualityCursorPlan() string {
	return strings.Replace(qualityListPlan(), `"result": "$steps.page"`, `"result": {"rows": "$steps.page", "nextCursor": "$steps.page.nextCursor", "total": "$steps.page.total"}`, 1)
}

// The summary uses its own, broader call policy, and returns only its count.
func qualitySummaryPlan() string {
	return `{"database": "crm", "params": {` + joinedParams + `}, ` + joinedAccess + `, "steps": [
    {"id": "matches", "op": "related_read", "max_rows": 1, "args": {
      "collection": "reports", "where": ` + joinedReportWhere + `,
      "select": ["id"], "count": true,
      "relation": ` + joinedContext(joinedSummaryCalls) + `, "match": ` + joinedSearch + `},
     "page": {"size": 1}}
  ], "result": [{"report_total": "$steps.matches.total"}]}`
}

const joinedPlayback = `{"database": "crm", "params": {"id": {"from": "$args.id", "type": "string", "required": true}},
  "access": {"require_any_permission": ["recording:read"]},
  "steps": [{"id": "call", "op": "find", "max_rows": 1, "args": {"collection": "calls", "select": ["audio_files"], "where": {"field": "local_id", "op": "eq", "value": "$param.id"}}}],
  "result": "$steps.call"}`

// Activity admin: an offer-only Quality account gets nothing here, as an empty
// scope, while its Quality resolvers keep working.
const activityAccess = `"access": {"values": {
    "centres": {"unrestricted_if_permission": "all_centres", "optional": true, "requested": "$param.centreIds",
      "default": {"collection": "centres", "field": "local_id"},
      "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "activity": {"optional": true, "expr": {"op": "if",
      "condition": {"op": "in", "args": [{"const": "quality"}, "$param.perms"]},
      "then": {"op": "if", "condition": {"op": "in", "args": [{"const": "admin"}, "$param.perms"]}, "then": "$scope.centres", "else": {"const": []}},
      "else": "$scope.centres"}}
  }}`

func activityPlan() string {
	return `{"database": "crm", "params": {` + joinedParams + `,
    "centreIds": {"from": "$args.centreIds", "type": "string_list", "encoding": "json"}}, ` + activityAccess + `, "steps": [
    {"id": "page", "op": "joined_page", "max_rows": 250, "args": {
      "collection": "calls",
      "where": {"field": "centre_id", "op": "in", "value": "$scope.activity"},
      "joins": [{"as": "p", "collection": "prospects", "kind": "left", "on": [{"local": "root.prospect_id", "foreign": "id"}]}],
      "computed": [{"name": "interaction", "expr": {"coalesce": [{"nullIfEmpty": {"field": "root.interaction_id"}}, {"field": "root.local_id"}]}}],
      "filter": {"and": [{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"field": "p.name", "op": "contains_ascii", "value": "$param.q"}}]},
      "group": {"by": ["interaction"], "orderBy": [{"field": "root.started_at", "direction": "desc", "agg": "max"}]},
      "orderBy": [{"field": "root.id", "direction": "desc"}],
      "select": ["root.id", "root.local_id", "p.name", "root.attempt_number"]},
     "page": {"size": "$param.limit", "offset": "$param.offset", "cursor": "$param.cursor"},
     "shape": {"rename": {"root.id": "id", "root.local_id": "local_id", "p.name": "display_prospect_name", "root.attempt_number": "attempt_number"},
               "total_as": "total_count", "numbers": ["id", "attempt_number"]}}
  ], "result": "$steps.page"}`
}

const costsPlan = `{"database": "crm", "params": {
    "user": {"from": "$identity.subject", "type": "string", "required": true},
    "perms": {"from": "$identity.permissions", "type": "string_list"},
    "from": {"from": "$args.dateFrom", "type": "string", "required": true}},
  "access": {"values": {"financial": {"optional": true, "expr": {"op": "if",
      "condition": {"op": "in", "args": [{"const": "economics"}, "$param.perms"]},
      "then": {"const": ["yes"]}, "else": {"const": []}}}}},
  "steps": [{"id": "costs", "op": "joined_aggregate", "max_rows": 1000, "args": {
      "collection": "costs",
      "where": {"and": [{"field": "day", "op": "gte", "value": "$param.from"}, {"field": "visible", "op": "in", "value": "$scope.financial"}]},
      "joins": [{"as": "c", "collection": "campaigns", "kind": "left", "on": [{"local": "root.campaign", "foreign": "id"}]}],
      "groupBy": ["root.day", "c.name"],
      "metrics": [{"name": "spend_micros", "op": "sum", "field": "root.spend"}, {"name": "conversions", "op": "sum", "field": "root.conversions"}]},
     "shape": {"rename": {"root.day": "day", "c.name": "campaign"}}}],
  "combine": {"masked": {"join": ["costs"], "on": ["day", "campaign"], "keep": ["costs"], "fields": {
      "conversions": {"op": "if", "condition": {"op": "in", "args": [{"const": "all_centres"}, "$param.perms"]}, "then": "costs.conversions", "else": {"const": null}}}}},
  "result": [{"payload": {"$json_text": "$combine.masked"}}]}`

const exportPlan = `{"database": "crm", "params": {
    "user": {"from": "$identity.subject", "type": "string", "required": true},
    "perms": {"from": "$identity.permissions", "type": "string_list"},
    "limit": {"from": "$args.limit", "type": "number"},
    "offset": {"from": "$args.offset", "type": "number"}},
  "steps": [{"id": "calls", "op": "joined_page", "max_rows": 100, "args": {
      "collection": "calls", "where": {"field": "direction", "op": "eq", "value": "inbound"},
      "orderBy": [{"field": "root.id", "direction": "desc"}],
      "select": ["root.id", "root.status"]},
     "page": {"size": "$param.limit", "offset": "$param.offset"},
     "shape": {"rename": {"root.id": "id", "root.status": "status"}, "numbers": ["id"]}}],
  "result": {"$if": {"op": "in", "args": [{"const": "admin"}, "$param.perms"]},
     "then": [{"payload": {"$json_text": {"total": "$steps.calls.total", "items": "$steps.calls"}}}],
     "else": []}}`

const joinedSDL = `
scalar JSON
type Query {
  qualityReportsPage(limit: Int = 25, offset: Int = 0, centreId: String = "", q: String = "", subject: String = "", cursor: String = ""): [Report!]!
  qualityCursor(limit: Int = 25, offset: Int = 0, centreId: String = "", q: String = "", cursor: String = ""): ReportPage!
  qualitySummary(limit: Int = 1, offset: Int = 0, centreId: String = "", q: String = "", cursor: String = ""): [Summary!]!
  playback(id: String!): [Recording!]!
  operationsCallsPage(limit: Int = 20, offset: Int = 0, centreId: String = "", q: String = "", cursor: String = "", centreIds: String = "[]"): [Call!]!
  costs(dateFrom: String!): [Payload!]!
  crmCallExport(limit: Int = 100, offset: Int = 0): [Payload!]!
}
type Report { id: ID local_id: ID score_total: Float total_count: Float display_context: JSON }
type ReportPage { rows: [Report!]! nextCursor: String total: Float }
type Summary { report_total: Float }
type Recording { audio_files: JSON }
type Call { id: ID local_id: ID display_prospect_name: String attempt_number: Float total_count: Float }
type Payload { payload: String! }
`

func joinedResolvers() map[string]string {
	return map[string]string{
		"qualityReportsPage":  qualityListPlan(),
		"qualityCursor":       qualityCursorPlan(),
		"qualitySummary":      qualitySummaryPlan(),
		"playback":            joinedPlayback,
		"operationsCallsPage": activityPlan(),
		"costs":               costsPlan,
		"crmCallExport":       exportPlan,
	}
}

type joinedFixture struct {
	t      *testing.T
	app    *App
	db     *fakeDatabase
	source int64
	schema int
}

func newJoinedFixture(t *testing.T, resolvers map[string]string) *joinedFixture {
	t.Helper()
	f := &fakeDatabase{commits: make(chan []string, 8)}
	f.respond = func(_ context.Context, op string, args map[string]any) (any, error) { return emptyAnswer(op), nil }
	a := secureTestApp(t, f)
	db := a.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", joinedSDL, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	source, err := createSourceForAPI(db, "p1", "default", "crm", "database", map[string]any{"database": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &joinedFixture{t: t, app: a, db: f, source: source.ID, schema: schema.Version}
	for field, plan := range resolvers {
		fixture.resolver(field, plan)
	}
	return fixture
}

func emptyAnswer(op string) map[string]any {
	switch op {
	case joinedRelatedRead, "find":
		return map[string]any{"records": []any{}, "hasMore": false, "total": "0"}
	case joinedPage:
		return map[string]any{"records": []any{}, "hasMore": false, "total": "0"}
	}
	return map[string]any{"rows": []any{}, "truncated": false}
}

func (f *joinedFixture) resolver(field, plan string) {
	f.t.Helper()
	if _, err := upsertResolverForAPI(f.app.ctx.AppDB(), "p1", "default", "Query", field, databasePipelineOperation, f.source, decodeConfig(f.t, plan)); err != nil {
		f.t.Fatal(field, err)
	}
}

func (f *joinedFixture) storeResolver(field, plan string) error {
	f.t.Helper()
	_, err := upsertResolverForAPI(f.app.ctx.AppDB(), "p1", "default", "Query", field, databasePipelineOperation, f.source, decodeConfig(f.t, plan))
	return err
}

// storeRaw puts a plan in the draft without the checks a save runs, as an older
// version of the app could have saved it.
func (f *joinedFixture) storeRaw(field, plan string) {
	f.t.Helper()
	result, err := f.app.ctx.AppDB().Exec(`UPDATE graphql_resolvers SET config_json = ? WHERE project_id = ? AND parent_type = 'Query' AND field_name = ?`, plan, storageProject("p1", "default"), field)
	if err != nil {
		f.t.Fatal(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		f.t.Fatalf("stored %d rows for %s", count, field)
	}
}

func (f *joinedFixture) publish() error {
	_, err := f.app.toolReleasePublish(context.Background(), f.app.ctx, map[string]any{"project_id": "p1", "environment": "production", "schema_version": f.schema})
	return err
}

func (f *joinedFixture) read(ctx context.Context, query string, variables map[string]any) (any, error) {
	f.t.Helper()
	out, err := f.app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		raw, _ := json.Marshal(out.Errors)
		return nil, errors.New(string(raw))
	}
	return out.Data, nil
}

func (f *joinedFixture) mustRead(ctx context.Context, query string, variables map[string]any) any {
	f.t.Helper()
	data, err := f.read(ctx, query, variables)
	if err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return data
}

// ---- resolver support and publish validation ----

func TestJoinedPublishProbesEveryBranchWithoutReadingRows(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, batch := range f.db.sent() {
		for _, operation := range batch {
			op, _ := operation["op"].(string)
			if !isJoinedOp(op) {
				continue
			}
			seen[op] = true
			args := operation["args"].(map[string]any)
			raw, _ := json.Marshal(args)
			text := string(raw)
			// The root matches nothing, whatever a client would send.
			if !filterMatchesNothing(args["where"]) {
				t.Errorf("%s probe can read rows: %s", op, text)
			}
			if args["limit"] != float64(1) {
				t.Errorf("%s probe limit: %v", op, args["limit"])
			}
			if strings.Contains(text, "$") {
				t.Errorf("%s probe holds an unbound placeholder: %s", op, text)
			}
		}
	}
	if !seen[joinedRelatedRead] || !seen[joinedPage] || !seen[joinedAggregate] {
		t.Fatalf("probed: %v", seen)
	}
	// Branches a false condition would remove are still checked: the search fields
	// and the offer restriction both reach Database.
	probe, err := joinedProbeOf(t, qualityListPlan())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(probe)
	for _, must := range []string{`"field":"client_name","op":"contains_ascii","value":""`, `"field":"offer_id","op":"is_null"`, `"field":"centre_id","op":"is_null"`, `"limit":40`} {
		if !strings.Contains(string(raw), must) {
			t.Errorf("probe lacks %s: %s", must, raw)
		}
	}
}

func joinedProbeOf(t *testing.T, config string) (map[string]any, error) {
	t.Helper()
	plan, err := validateDatabasePipeline(decodeConfig(t, config))
	if err != nil {
		t.Fatal(err)
	}
	return plan.Steps[0].joinedProbe()
}

// The fake Database refuses what the real one would, so a publish failure proves
// GraphQL asked, and the earlier release keeps answering.
func TestJoinedInvalidRevisionLeavesThePreviousReleaseActive(t *testing.T) {
	f := newJoinedFixture(t, map[string]string{"qualityReportsPage": qualityListPlan()})
	f.db.respond = func(_ context.Context, op string, args map[string]any) (any, error) {
		if isJoinedOp(op) && !filterMatchesNothing(args["where"]) {
			return map[string]any{"records": []any{map[string]any{"id": "7", "local_id": "r7", "score_total": "4", "display_context": []any{}}}, "hasMore": false, "total": "1"}, nil
		}
		if args["collection"] == "centres" {
			return map[string]any{"records": []any{map[string]any{"local_id": "a"}}, "hasMore": false}, nil
		}
		return emptyAnswer(op), nil
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	admin := viewer("50", "all_centres")
	const query = `{qualityReportsPage{id local_id score_total total_count}}`
	const previous = `{"qualityReportsPage":[{"id":"7","local_id":"r7","score_total":4,"total_count":1}]}`
	sameJSON(t, "first release", f.mustRead(admin, query, nil), previous)

	edit := func(old, replacement string) string {
		t.Helper()
		plan := qualityListPlan()
		if !strings.Contains(plan, old) {
			t.Fatalf("plan lacks %s", old)
		}
		return strings.Replace(plan, old, replacement, 1)
	}
	databaseRefusal := func(check func(op string, args map[string]any) error) func(string, map[string]any) error {
		return check
	}
	for name, c := range map[string]struct {
		plan   string
		refuse func(string, map[string]any) error
		want   string
	}{
		"Database refuses the operation": {plan: qualityListPlan(), refuse: databaseRefusal(func(op string, _ map[string]any) error {
			if isJoinedOp(op) {
				return errors.New("unknown operation " + op)
			}
			return nil
		}), want: "require Database 0.12.0"},
		"incompatible join keys": {plan: edit(`, "comparison": "text"}]}]},`, `}]}]},`), refuse: databaseRefusal(func(op string, args map[string]any) error {
			raw, _ := json.Marshal(args)
			if isJoinedOp(op) && strings.Contains(string(raw), `{"foreign":"report_id","local":"root.id"}`) {
				return errors.New("join l compares root.id (integer) with report_id (text); use a comparison or fields of one type")
			}
			return nil
		}), want: "compares root.id"},
		"missing index": {plan: qualityListPlan(), refuse: databaseRefusal(func(op string, _ map[string]any) error {
			if isJoinedOp(op) {
				return errors.New("query_too_expensive: no index on report_id")
			}
			return nil
		}), want: "query_too_expensive"},
		"unbounded relation":            {plan: edit(`"as": "display_context", "limit": 40,`, `"as": "display_context",`), want: "relation limit"},
		"oversized relation":            {plan: edit(`"limit": 40,`, `"limit": 100000,`), want: "relation limit"},
		"client context cap":            {plan: edit(`"limit": 40,`, `"limit": "$param.limit",`), want: "relation limit"},
		"client value in a join":        {plan: edit(`"where": `+joinedListCalls, `"where": {"field": "offer_id", "op": "eq", "value": "$param.q"}`), want: "client input"},
		"client when in a join":         {plan: edit(`"where": `+joinedListCalls, `"where": {"and": [{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"field": "offer_id", "op": "eq", "value": "x"}}]}`), want: "client when"},
		"client value in eligibility":   {plan: edit(`"orderBy": [{"field": "s.id", "direction": "asc"}],`, `"filter": {"field": "s.offer_id", "op": "eq", "value": "$param.q"},`), want: "client input"},
		"client chooses a field":        {plan: edit(`"select": ["id", "local_id", "score_total"]`, `"select": ["id", "$param.q"]`), want: "placeholders"},
		"client chooses a collection":   {plan: edit(`"collection": "sale_links"`, `"collection": "$param.q"`), want: "placeholders"},
		"client chooses a join key":     {plan: edit(`"foreign": "local_id"}],`, `"foreign": "$param.q"}],`), want: "placeholders"},
		"client chooses a computed":     {plan: edit(`{"trim": {"field": "s.client"}}`, `{"const": "$param.q"}`), want: "placeholders"},
		"client value beside authority": {plan: edit(`{"field": "centre_id", "op": "in", "value": "$scope.centres"},`, `{"or": [{"field": "centre_id", "op": "in", "value": "$scope.centres"}, {"field": "centre_id", "op": "eq", "value": "$param.centre"}]},`), want: "widen"},
		"unguarded search on the rows":  {plan: edit(`"match": `+joinedSearch, `"match": {"field": "phone", "op": "contains_ascii", "value": "$param.q"}`), want: "must sit in a when"},
		"relationship in match":         {plan: edit(`"match": `+joinedSearch, `"match": {"exists": {"collection": "sales", "on": [{"localField": "id", "foreignField": "id"}]}}`), want: "exists"},
		"unknown relation key":          {plan: edit(`"as": "display_context", "limit": 40,`, `"as": "display_context", "limit": 40, "cap": 1,`), want: "cap"},
		"unknown step key":              {plan: edit(`"page": {"size"`, `"limit": 5, "page": {"size"`), want: "limit"},
		"unknown arg":                   {plan: edit(`"count": true,`, `"count": true, "groupBy": ["id"],`), want: "does not accept"},
		"argument of the wrong type":    {plan: edit(`"offset": {"from": "$args.offset", "type": "number"}`, `"offset": {"from": "$args.centreId", "type": "number"}`), want: "cannot supply"},
		"argument not on the field":     {plan: edit(`"q": {"from": "$args.q", "type": "string"}`, `"q": {"from": "$args.nope", "type": "string"}`), want: "does not declare"},
		"total_as without a count":      {plan: edit(`"count": true,`, ``), want: "count"},
	} {
		t.Run(name, func(t *testing.T) {
			// A plan the app can judge alone is refused on save, and again if a stored
			// one reaches publish. What only Database can judge fails at publish.
			f.db.refuse = c.refuse
			err := f.storeResolver("qualityReportsPage", c.plan)
			if err != nil {
				if !strings.Contains(err.Error(), c.want) {
					t.Fatalf("save: %v, want %q", err, c.want)
				}
				f.storeRaw("qualityReportsPage", c.plan)
			}
			err = f.publish()
			f.db.refuse = nil
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("publish: %v, want %q", err, c.want)
			}
			sameJSON(t, "the earlier release", f.mustRead(admin, query, nil), previous)
		})
	}
	// The draft is still broken; correcting it publishes again.
	f.resolver("qualityReportsPage", qualityListPlan())
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
}

func TestJoinedPlanRefusesClientValuesOutsideNarrowingSlots(t *testing.T) {
	base := func(inner string) string {
		return `{"database": "crm", "params": {"q": {"from": "$args.q", "type": "string"}, "user": {"from": "$identity.subject", "type": "string"}},
		  "steps": [` + inner + `], "result": "$steps.s"}`
	}
	join := func(where string) string {
		return `{"as": "j", "collection": "b", "on": [{"local": "root.id", "foreign": "a_id"}], "where": ` + where + `}`
	}
	client := `{"field": "x", "op": "eq", "value": "$param.q"}`
	for name, plan := range map[string]string{
		"join where":          base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"], "joins": [` + join(client) + `]}}`),
		"join metric":         base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"], "joins": [{"as": "j", "kind": "aggregate", "collection": "b", "on": [{"local": "root.id", "foreign": "a_id"}], "metrics": [{"name": "n", "op": "count", "where": ` + client + `}]}]}}`),
		"pick":                base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "pick": {"by": ["root.id"], "orderBy": [{"field": "root.id"}], "where": ` + client + `}, "metrics": [{"name": "n", "op": "count"}]}}`),
		"rollup filter":       base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "rollup": {"by": ["root.id"], "metrics": [{"name": "n", "op": "count"}], "filter": ` + client + `}, "metrics": [{"name": "m", "op": "count"}]}}`),
		"rollup metric":       base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "rollup": {"by": ["root.id"], "metrics": [{"name": "n", "op": "count", "where": ` + client + `}]}, "metrics": [{"name": "m", "op": "count"}]}}`),
		"computed if":         base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["c"], "computed": [{"name": "c", "expr": {"if": ` + client + `, "then": {"const": 1}, "else": {"const": 0}}}]}}`),
		"nested computed if":  base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["c"], "computed": [{"name": "c", "expr": {"coalesce": [{"if": ` + client + `, "then": {"const": 1}}]}}]}}`),
		"branch join":         base(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [{"collection": "a", "select": ["root.id"], "joins": [` + join(client) + `]}], "metrics": [{"name": "n", "op": "count"}]}}`),
		"branch pick":         base(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [{"collection": "a", "select": ["root.id"], "pick": {"by": ["root.id"], "orderBy": [{"field": "root.id"}], "where": ` + client + `}}], "metrics": [{"name": "n", "op": "count"}]}}`),
		"source filter":       base(`{"id": "s", "op": "related_read", "args": {"collection": "a", "select": ["id"], "relation": {"as": "r", "limit": 1, "sources": [{"name": "b", "joins": [` + join(`{"field":"y","op":"eq","value":"1"}`) + `], "filter": ` + client + `, "select": ["j.y"]}]}}}`),
		"group by":            base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"], "group": {"by": ["$param.q"]}}}`),
		"order":               base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"], "orderBy": [{"field": "$param.q"}]}}`),
		"derived":             base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "metrics": [{"name": "n", "op": "count"}], "derived": [{"name": "d", "op": "divide", "numerator": "$param.q", "denominator": "n"}]}}`),
		"root alias":          base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "as": "$param.q", "select": ["id"]}}`),
		"no select":           base(`{"id": "s", "op": "joined_page", "args": {"collection": "a"}}`),
		"both inputs":         base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "branches": [{"collection": "a", "select": ["root.id"]}], "metrics": [{"name": "n", "op": "count"}]}}`),
		"unsupported op":      base(`{"id": "s", "op": "joined_scan", "args": {"collection": "a"}}`),
		"literal offset cap":  base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"]}, "page": {"offset": 100001}}`),
		"offset from a text":  base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"]}, "page": {"offset": "$param.q"}}`),
		"offset on find":      base(`{"id": "s", "op": "find", "args": {"collection": "a"}, "page": {"offset": 1}}`),
		"shape on find":       base(`{"id": "s", "op": "find", "args": {"collection": "a"}, "shape": {"total_as": "t"}}`),
		"rename to bad name":  base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"]}, "shape": {"rename": {"root.id": "a.b"}}}`),
		"rename collision":    base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id", "root.x"]}, "shape": {"rename": {"root.id": "id", "root.x": "id"}}}`),
		"$if from the client": `{"database": "crm", "params": {"q": {"from": "$args.q", "type": "string"}}, "steps": [{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"]}}], "result": {"$if": {"op": "eq", "args": ["$param.q", {"const": "x"}]}, "then": [], "else": []}}`,
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, plan)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// The narrowing slots do take client values, in the forms that only narrow.
	for name, plan := range map[string]string{
		"root where":   base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "where": ` + client + `, "select": ["root.id"]}}`),
		"joined rows":  base(`{"id": "s", "op": "joined_page", "args": {"collection": "a", "filter": ` + client + `, "select": ["root.id"]}}`),
		"outer metric": base(`{"id": "s", "op": "joined_aggregate", "args": {"collection": "a", "metrics": [{"name": "n", "op": "count", "where": ` + client + `}]}}`),
		"branch where": base(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [{"collection": "a", "where": ` + client + `, "filter": ` + client + `, "select": ["root.id"]}], "metrics": [{"name": "n", "op": "count"}]}}`),
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, plan)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestJoinedProbeCoversEachBranch(t *testing.T) {
	plan, err := validateDatabasePipeline(decodeConfig(t, `{"database": "crm", "steps": [{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "as": "x", "select": ["x.id"]},
	  {"collection": "b", "where": {"field": "day", "op": "gte", "value": "2026"}, "select": ["root.id"]}],
	  "metrics": [{"name": "n", "op": "count"}]}}], "result": "$steps.s"}`))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := plan.Steps[0].joinedProbe()
	if err != nil {
		t.Fatal(err)
	}
	branches := probe["args"].(map[string]any)["branches"].([]any)
	for i, raw := range branches {
		if !filterMatchesNothing(raw.(map[string]any)["where"]) {
			t.Errorf("branch %d can read rows: %v", i, raw)
		}
	}
	sameJSON(t, "first branch names its alias's field", branches[0].(map[string]any)["where"], `{"and":[{"field":"id","op":"is_null"},{"field":"id","op":"is_not_null"}]}`)
}

// ---- trusted identity and per-resolver permissions ----

// grantsDatabase answers access reads for four local identities.
func grantsDatabase(rows map[string]func(args map[string]any) any) func(context.Context, string, map[string]any) (any, error) {
	return func(_ context.Context, op string, args map[string]any) (any, error) {
		records := func(field string, values ...string) any {
			out := []any{}
			for _, value := range values {
				out = append(out, map[string]any{field: value})
			}
			return map[string]any{"records": out, "hasMore": false}
		}
		user := ""
		if where, isFilter := args["where"].(map[string]any); isFilter {
			user, _ = where["value"].(string)
		}
		switch args["collection"] {
		case "centres":
			return records("local_id", "a", "b"), nil
		case "profiles":
			switch user {
			case "21": // centre-scoped
				return records("centre_id", "a"), nil
			case "22": // offer-only
				return records("centre_id", "a"), nil
			}
			return records("centre_id"), nil
		case "grants":
			if user == "22" {
				return map[string]any{"records": []any{map[string]any{"offer_ids": []any{"energy"}}}, "hasMore": false}, nil
			}
			return records("offer_ids"), nil
		}
		if read := rows[op]; read != nil {
			return read(args), nil
		}
		return emptyAnswer(op), nil
	}
}

const qualityFields = `id local_id score_total total_count display_context`

var (
	allCentres   = func() context.Context { return viewer("20", "all_centres") }
	centreScoped = func() context.Context { return viewer("21", "manager") }
	offerOnly    = func() context.Context { return viewer("22", "quality") }
	deniedViewer = func() context.Context { return viewer("23") }
)

func TestJoinedSendsExactlyTheTrustedScope(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(nil)
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	const query = `query($c: String, $q: String) {qualityReportsPage(centreId: $c, q: $q){` + qualityFields + `}}`
	none := `{"and":[{"and":[{"field":"centre_id","op":"is_null"},{"field":"centre_id","op":"is_not_null"}]}]}`
	for name, c := range map[string]struct {
		ctx       func() context.Context
		where     string
		saleWhere string
	}{
		"all centres":      {allCentres, `{"and":[{"field":"centre_id","op":"in","value":["a","b"]}]}`, ``},
		"centre scoped":    {centreScoped, `{"and":[{"field":"centre_id","op":"in","value":["a"]}]}`, ``},
		"offer only":       {offerOnly, `{"and":[{"field":"centre_id","op":"in","value":["a"]}]}`, `{"and":[{"field":"offer_id","op":"in","value":["energy"]}]}`},
		"no grants at all": {deniedViewer, none, ``},
	} {
		t.Run(name, func(t *testing.T) {
			f.db.forget()
			sameJSON(t, "response", f.mustRead(c.ctx(), query, nil), `{"qualityReportsPage":[]}`)
			args := f.db.lastJoined(t)
			sameJSON(t, "root where", args["where"], c.where)
			sources := args["relation"].(map[string]any)["sources"].([]any)
			sale := sources[0].(map[string]any)["joins"].([]any)[1].(map[string]any)
			if c.saleWhere == "" {
				if _, found := sale["where"]; found {
					t.Errorf("an unrestricted viewer got an offer filter: %v", sale["where"])
				}
			} else {
				sameJSON(t, "offer restriction", sale["where"], c.saleWhere)
			}
			if _, found := args["match"]; found {
				t.Errorf("a blank q still searched: %v", args["match"])
			}
			if args["relation"].(map[string]any)["limit"] != float64(40) {
				t.Errorf("cap: %v", args["relation"])
			}
		})
	}

	// A client filter only adds a restriction next to the scope.
	f.db.forget()
	f.mustRead(centreScoped(), query, map[string]any{"c": "b", "q": "100%"})
	args := f.db.lastJoined(t)
	sameJSON(t, "a centre outside scope only narrows", args["where"], `{"and":[{"field":"centre_id","op":"in","value":["a"]},{"field":"centre_id","op":"eq","value":"b"}]}`)
	raw, _ := json.Marshal(args["match"])
	if !strings.Contains(string(raw), `"value":"100%"`) {
		t.Errorf("the search value did not reach match: %s", raw)
	}

	// A forged subject argument is never read: the profile read uses the verified one.
	f.db.forget()
	f.mustRead(centreScoped(), `{qualityReportsPage(subject: "20"){id}}`, nil)
	for _, batch := range f.db.sent() {
		for _, operation := range batch {
			operationArgs := operation["args"].(map[string]any)
			if operationArgs["collection"] == "profiles" {
				sameJSON(t, "profile read", operationArgs["where"], `{"field":"user_id","op":"eq","value":"21"}`)
			}
		}
	}
}

func TestJoinedActivityAdminStaysDeniedForOfferOnlyRole(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedPage: func(args map[string]any) any {
		if filterMatchesNothing(args["where"]) {
			return map[string]any{"groups": []any{}, "hasMore": false, "total": "0"}
		}
		return map[string]any{"groups": []any{map[string]any{"key": map[string]any{"interaction": "i1"}, "rows": []any{map[string]any{"root.id": "2"}}}}, "hasMore": false, "total": "1"}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	const query = `{operationsCallsPage{id total_count}}`
	sameJSON(t, "all centres", f.mustRead(allCentres(), query, nil), `{"operationsCallsPage":[{"id":"2","total_count":1}]}`)
	sameJSON(t, "centre scoped", f.mustRead(centreScoped(), query, nil), `{"operationsCallsPage":[{"id":"2","total_count":1}]}`)
	f.db.forget()
	sameJSON(t, "offer only", f.mustRead(offerOnly(), query, nil), `{"operationsCallsPage":[]}`)
	if !filterMatchesNothing(f.db.lastJoined(t)["where"]) {
		t.Errorf("offer-only reached rows: %v", f.db.lastJoined(t)["where"])
	}
	sameJSON(t, "no grants", f.mustRead(deniedViewer(), query, nil), `{"operationsCallsPage":[]}`)
	// The same role keeps its Quality access, under its own policy.
	f.db.forget()
	f.mustRead(offerOnly(), `{qualityReportsPage{id}}`, nil)
	if filterMatchesNothing(f.db.lastJoined(t)["where"]) {
		t.Error("the Quality resolver inherited the Activity denial")
	}
	// A requested centre must be inside what the viewer may request.
	if _, err := f.read(centreScoped(), `{operationsCallsPage(centreIds: "[\"b\"]"){id}}`, nil); err == nil || !strings.Contains(err.Error(), "outside the caller's access") {
		t.Errorf("a centre outside scope: %v", err)
	}
	if _, err := f.read(allCentres(), `{operationsCallsPage(centreIds: "not json"){id}}`, nil); err == nil || !strings.Contains(err.Error(), "JSON list") {
		t.Errorf("malformed centres: %v", err)
	}
	f.db.forget()
	f.mustRead(allCentres(), `{operationsCallsPage(centreIds: "[\"b\", 7]"){id}}`, nil)
	sameJSON(t, "requested centres", f.db.lastJoined(t)["where"], `{"field":"centre_id","op":"in","value":["b","7"]}`)
}

// The list and the summary apply different call policies, so a mixed-offer match
// can count in the summary and be absent from the list. The summary never returns
// the context or audio behind its count.
func TestJoinedSummaryPolicyStaysSeparateAndExposesNoContext(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	secret := []any{map[string]any{"client_name": "hidden name", "phone": "0600000000", "audio_files": []any{"call.mp3"}}}
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedRelatedRead: func(args map[string]any) any {
		raw, _ := json.Marshal(args["relation"])
		if strings.Contains(string(raw), "offer_mix") { // the list's narrower policy
			return map[string]any{"records": []any{}, "hasMore": false, "total": "0"}
		}
		return map[string]any{"records": []any{map[string]any{"id": "100", "display_context": secret}}, "hasMore": false, "total": "1"}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	const q = `(q: "mixed")`
	sameJSON(t, "list", f.mustRead(offerOnly(), `{qualityReportsPage`+q+`{id display_context}}`, nil), `{"qualityReportsPage":[]}`)
	summary := f.mustRead(offerOnly(), `{qualitySummary`+q+`{report_total}}`, nil)
	sameJSON(t, "summary", summary, `{"qualitySummary":[{"report_total":1}]}`)
	raw, _ := json.Marshal(summary)
	for _, leak := range []string{"hidden name", "0600000000", "call.mp3"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the summary exposed %s: %s", leak, raw)
		}
	}
	if _, err := f.read(offerOnly(), `{qualitySummary`+q+`{report_total display_context}}`, nil); err == nil {
		t.Error("the summary's type offers its context")
	}
	// A summary match grants nothing on playback, whose policy is its own.
	if _, err := f.read(offerOnly(), `{playback(id: "c1"){audio_files}}`, nil); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Errorf("playback after a summary match: %v", err)
	}
}

// ---- existing response contracts ----

func TestJoinedQualityResponseMatchesTheSavedShape(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedRelatedRead: func(args map[string]any) any {
		context := []any{
			map[string]any{"client_name": "Ana & Co <x>", "phone": "33600", "source": "sales"},
			map[string]any{"client_name": "Bob", "phone": "33601", "source": "calls"},
		}
		return map[string]any{"total": "7", "hasMore": true, "records": []any{
			map[string]any{"id": "9", "local_id": "r9", "score_total": "12", "display_context": context},
			map[string]any{"id": "8", "local_id": "r8", "score_total": nil, "display_context": []any{}},
		}}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	got := f.mustRead(allCentres(), `{qualityReportsPage(limit: 2, offset: 3){`+qualityFields+`}}`, nil)
	sameJSON(t, "whole response", got, `{"qualityReportsPage":[
	  {"id":"9","local_id":"r9","score_total":12,"total_count":7,"display_context":"[{\"client_name\":\"Ana & Co <x>\",\"phone\":\"33600\"},{\"client_name\":\"Bob\",\"phone\":\"33601\"}]"},
	  {"id":"8","local_id":"r8","score_total":null,"total_count":7,"display_context":"[]"}]}`)
	args := f.db.lastJoined(t)
	if args["limit"] != float64(2) || args["offset"] != float64(3) || args["count"] != true {
		t.Errorf("paging: limit %v offset %v count %v", args["limit"], args["offset"], args["count"])
	}
	if _, found := args["cursor"]; found {
		t.Errorf("an offset page sent a cursor: %v", args["cursor"])
	}
}

func TestJoinedActivityKeepsInteractionCountAcrossLegs(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedPage: func(map[string]any) any {
		return map[string]any{"total": "4", "hasMore": true, "groups": []any{map[string]any{"key": map[string]any{"interaction": "i1"}, "rows": []any{
			map[string]any{"root.id": "2", "root.local_id": "child", "p.name": "Ana", "root.attempt_number": "1"},
			map[string]any{"root.id": "1", "root.local_id": "parent", "p.name": nil, "root.attempt_number": "2"},
		}}}}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	got := f.mustRead(allCentres(), `{operationsCallsPage(limit: 1){id local_id display_prospect_name attempt_number total_count}}`, nil)
	sameJSON(t, "two legs of one interaction", got, `{"operationsCallsPage":[
	  {"id":"2","local_id":"child","display_prospect_name":"Ana","attempt_number":1,"total_count":4},
	  {"id":"1","local_id":"parent","display_prospect_name":null,"attempt_number":2,"total_count":4}]}`)
	if limit := f.db.lastJoined(t)["limit"]; limit != float64(1) {
		t.Errorf("the page counts interactions, so its limit stays 1: %v", limit)
	}
}

func TestJoinedEmptyPagesAndEnvelopes(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{
		joinedRelatedRead: func(map[string]any) any {
			return map[string]any{"total": "3", "hasMore": false, "records": []any{}}
		},
		joinedAggregate: func(args map[string]any) any {
			raw, _ := json.Marshal(args["where"])
			if strings.Contains(string(raw), "is_not_null") { // no financial permission
				return map[string]any{"rows": []any{}, "truncated": false}
			}
			return map[string]any{"truncated": false, "rows": []any{
				map[string]any{"root.day": "2026-10-01", "c.name": "A", "spend_micros": "9007199254740993", "conversions": "4"},
			}}
		},
	})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "a page past the end", f.mustRead(allCentres(), `{qualityReportsPage(offset: 100){id total_count}}`, nil), `{"qualityReportsPage":[]}`)
	const costs = `{costs(dateFrom: "2026-10-01"){payload}}`
	sameJSON(t, "no financial permission keeps the empty envelope", f.mustRead(allCentres(), costs, nil), `{"costs":[{"payload":"[]"}]}`)
	sameJSON(t, "a masked field stays hidden", f.mustRead(viewer("30", "economics"), costs, nil),
		`{"costs":[{"payload":"[{\"campaign\":\"A\",\"conversions\":null,\"day\":\"2026-10-01\",\"spend_micros\":\"9007199254740993\"}]"}]}`)
	sameJSON(t, "the full view", f.mustRead(viewer("31", "economics", "all_centres"), costs, nil),
		`{"costs":[{"payload":"[{\"campaign\":\"A\",\"conversions\":4,\"day\":\"2026-10-01\",\"spend_micros\":\"9007199254740993\"}]"}]}`)

	// A limit of 0 returns no rows, like SQL, without losing the total. Database has no
	// such page, so GraphQL asks for one row to learn the total and drops the row.
	withRows := f.db.respond
	f.db.respond = func(ctx context.Context, op string, args map[string]any) (any, error) {
		if op == joinedRelatedRead {
			if args["limit"] != float64(1) {
				t.Errorf("limit 0 asked for %v rows", args["limit"])
			}
			return map[string]any{"total": "3", "hasMore": true, "nextCursor": "native", "records": []any{map[string]any{"id": "9", "display_context": []any{}}}}, nil
		}
		return withRows(ctx, op, args)
	}
	got := f.mustRead(allCentres(), `{qualityCursor(limit: 0){rows{id} nextCursor total}}`, nil)
	sameJSON(t, "limit 0", got, `{"qualityCursor":{"rows":[],"nextCursor":null,"total":3}}`)
	f.db.respond = withRows
	// A negative offset starts at the first record, like SQL.
	f.db.forget()
	f.mustRead(allCentres(), `{qualityReportsPage(offset: -5){id}}`, nil)
	if _, found := f.db.lastJoined(t)["offset"]; found {
		t.Error("a negative offset reached Database")
	}
	if _, err := f.read(allCentres(), `{qualityReportsPage(offset: 100001){id}}`, nil); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("offset past Database's limit: %v", err)
	}
	if _, err := f.read(allCentres(), `{qualityReportsPage(limit: -1){id}}`, nil); err == nil {
		t.Error("a negative limit was accepted")
	}
}

func TestJoinedExportPayloadAndDeniedEmptyList(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedPage: func(map[string]any) any {
		return map[string]any{"total": "2", "hasMore": true, "records": []any{map[string]any{"root.id": "5", "root.status": "answered"}}}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "export", f.mustRead(viewer("1", "admin"), `{crmCallExport(limit: 1){payload}}`, nil),
		`{"crmCallExport":[{"payload":"{\"items\":[{\"id\":5,\"status\":\"answered\"}],\"total\":2}"}]}`)
	sameJSON(t, "denied", f.mustRead(viewer("2"), `{crmCallExport(limit: 1){payload}}`, nil), `{"crmCallExport":[]}`)
}

func TestJoinedResultsFailWholeRatherThanPartial(t *testing.T) {
	f := newJoinedFixture(t, map[string]string{
		"costs": costsPlan,
		"crmCallExport": `{"database": "crm", "steps": [{"id": "calls", "op": "joined_page", "max_rows": 2, "args": {"collection": "calls", "select": ["root.id"]}}],
		  "result": [{"payload": {"$json_text": "$steps.calls"}}]}`,
	})
	f.db.respond = func(_ context.Context, op string, _ map[string]any) (any, error) {
		if op == joinedAggregate {
			return map[string]any{"rows": []any{}, "truncated": true}, nil
		}
		return map[string]any{"records": []any{map[string]any{"root.id": "1"}}, "hasMore": true, "total": "9"}, nil
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.read(viewer("1", "economics"), `{costs(dateFrom: "x"){payload}}`, nil); err == nil || !strings.Contains(err.Error(), "max_rows") {
		t.Errorf("truncated aggregate: %v", err)
	}
	if _, err := f.read(viewer("1"), `{crmCallExport{payload}}`, nil); err == nil || !strings.Contains(err.Error(), "max_rows") {
		t.Errorf("unpaged read past its bound: %v", err)
	}
	// A read over Database's budget fails whole: no field gets partial rows.
	f.db.respond = func(_ context.Context, op string, _ map[string]any) (any, error) {
		if isJoinedOp(op) {
			return nil, errors.New("resource_limit: joined read exceeded its work budget")
		}
		return emptyAnswer(op), nil
	}
	out, err := f.app.execute(viewer("1"), "p1", "default", "production", graphqlRequest{Query: `{crmCallExport{payload}}`})
	if err == nil && len(out.Errors) == 0 {
		t.Fatalf("a read over budget returned %v", out.Data)
	}
	if rows, _ := out.Data["crmCallExport"].([]any); len(rows) > 0 {
		t.Errorf("a read over budget returned rows: %v", rows)
	}
}

// cursorFixture answers pages with a native cursor, and refuses one it did not issue.
func cursorFixture(t *testing.T) *joinedFixture {
	t.Helper()
	f := newJoinedFixture(t, joinedResolvers())
	f.db.respond = grantsDatabase(map[string]func(map[string]any) any{joinedRelatedRead: func(args map[string]any) any {
		if cursor, found := args["cursor"]; found && cursor != "native-1" {
			t.Errorf("Database got %v", cursor)
		}
		return map[string]any{"total": "3", "hasMore": true, "nextCursor": "native-1", "records": []any{map[string]any{"id": "9", "display_context": []any{}}}}
	}})
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	return f
}

const cursorQuery = `query($c: String, $centre: String) {qualityCursor(limit: 1, cursor: $c, centreId: $centre){rows{id} nextCursor total}}`

func firstCursor(t *testing.T, f *joinedFixture, ctx context.Context) string {
	t.Helper()
	data := f.mustRead(ctx, cursorQuery, nil).(map[string]any)["qualityCursor"].(map[string]any)
	cursor, _ := data["nextCursor"].(string)
	if !strings.HasPrefix(cursor, cursorPrefix) || strings.Contains(cursor, "native-1") {
		t.Fatalf("the page returned %q", cursor)
	}
	return cursor
}

func TestJoinedCursorWalksForTheSameRequest(t *testing.T) {
	f := cursorFixture(t)
	cursor := firstCursor(t, f, allCentres())
	f.db.forget()
	f.mustRead(allCentres(), cursorQuery, map[string]any{"c": cursor})
	args := f.db.lastJoined(t)
	if args["cursor"] != "native-1" {
		t.Errorf("Database got %v", args["cursor"])
	}
	if _, found := args["offset"]; found {
		t.Error("a cursor page also sent an offset")
	}
	// A refreshed token is the same viewer.
	refreshed := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "20", Permissions: []string{"all_centres"}, Expires: time.Now().Add(2 * time.Hour), Claims: map[string]any{"iat": 99}})
	f.mustRead(refreshed, cursorQuery, map[string]any{"c": cursor})
	if _, err := f.read(allCentres(), `query($c: String) {qualityCursor(cursor: $c, offset: 2){rows{id}}}`, map[string]any{"c": cursor}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("cursor and offset: %v", err)
	}
}

func TestJoinedCursorCannotBeReused(t *testing.T) {
	f := cursorFixture(t)
	cursor := firstCursor(t, f, allCentres())
	refused := func(what string, ctx context.Context, variables map[string]any) {
		t.Helper()
		f.db.forget()
		_, err := f.read(ctx, cursorQuery, variables)
		if err == nil || !strings.Contains(err.Error(), "cursor does not belong") {
			t.Errorf("%s: %v", what, err)
		}
		for _, batch := range f.db.sent() {
			for _, operation := range batch {
				if op, _ := operation["op"].(string); isJoinedOp(op) {
					t.Errorf("%s reached Database", what)
				}
			}
		}
	}
	refused("another viewer with the same access", viewer("99", "all_centres"), map[string]any{"c": cursor})
	refused("another scope", centreScoped(), map[string]any{"c": cursor})
	refused("another filter", allCentres(), map[string]any{"c": cursor, "centre": "a"})
	refused("changed permissions", viewer("20", "all_centres", "extra"), map[string]any{"c": cursor})
	refused("Database's own cursor", allCentres(), map[string]any{"c": "native-1"})
	refused("a forged signature", allCentres(), map[string]any{"c": cursor[:len(cursor)-2] + "AA"})
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, cursorPrefix))
	if err != nil {
		t.Fatal(err)
	}
	// What Database's cursor says about the rows stays hidden from the viewer.
	if strings.Contains(cursor, "native") || strings.Contains(string(raw), "native") {
		t.Errorf("the cursor shows Database's: %s", cursor)
	}
	raw[len(raw)-1] ^= 1
	refused("a changed cursor", allCentres(), map[string]any{"c": cursorPrefix + base64.RawURLEncoding.EncodeToString(raw)})
	refused("a cursor too short to hold anything", allCentres(), map[string]any{"c": cursorPrefix + "bmF0aXZlLTE"})

	// Changed grants change the scope, so a held cursor stops working.
	scoped := centreScoped()
	held := firstCursor(t, f, scoped)
	respond := f.db.respond
	f.db.respond = func(ctx context.Context, op string, args map[string]any) (any, error) {
		if args["collection"] == "profiles" {
			return map[string]any{"records": []any{map[string]any{"centre_id": "b"}}, "hasMore": false}, nil
		}
		return respond(ctx, op, args)
	}
	refused("after a grant change", scoped, map[string]any{"c": held})
	f.db.respond = respond

	// A new resolver version refuses the old plan's cursors, even when what it asks
	// Database is unchanged.
	f.resolver("qualityCursor", strings.Replace(qualityCursorPlan(), `"max_rows": 100`, `"max_rows": 99`, 1))
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	refused("under a new resolver version", allCentres(), map[string]any{"c": cursor})

	// Another project has its own key and binding.
	if _, err := f.app.openCursor("p2", "anything", cursor); err == nil {
		t.Error("another project opened the cursor")
	}
}

// A scope a page's own request doesn't mention still ends its cursor when it changes.
func TestJoinedCursorBindsScopeOutsideTheRequest(t *testing.T) {
	f := newJoinedFixture(t, map[string]string{"qualityCursor": `{"database": "crm",
	  "params": {"user": {"from": "$identity.subject", "type": "string", "required": true}, "cursor": {"from": "$args.cursor", "type": "string"}},
	  "access": {"values": {"centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
	  "steps": [
	    {"id": "page", "op": "joined_page", "max_rows": 5, "args": {"collection": "reports", "select": ["root.id"], "orderBy": [{"field": "root.id"}]}, "page": {"size": 1, "cursor": "$param.cursor"}, "shape": {"rename": {"root.id": "id"}}},
	    {"id": "visible", "op": "find", "max_rows": 5, "args": {"collection": "centres", "where": {"field": "local_id", "op": "in", "value": "$scope.centres"}}}],
	  "result": {"rows": "$steps.page", "nextCursor": "$steps.page.nextCursor"}}`})
	centre := "a"
	f.db.respond = func(_ context.Context, op string, args map[string]any) (any, error) {
		switch {
		case args["collection"] == "profiles":
			return map[string]any{"records": []any{map[string]any{"centre_id": centre}}, "hasMore": false}, nil
		case op == joinedPage:
			return map[string]any{"records": []any{map[string]any{"root.id": "1"}}, "hasMore": true, "nextCursor": "native", "total": "2"}, nil
		}
		return emptyAnswer(op), nil
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	const query = `query($c: String) {qualityCursor(cursor: $c){nextCursor}}`
	reader := viewer("21", "manager")
	data := f.mustRead(reader, query, nil).(map[string]any)["qualityCursor"].(map[string]any)
	cursor := data["nextCursor"].(string)
	f.mustRead(reader, query, map[string]any{"c": cursor})
	centre = "b"
	if _, err := f.read(reader, query, map[string]any{"c": cursor}); err == nil || !strings.Contains(err.Error(), "cursor does not belong") {
		t.Errorf("after the scope changed: %v", err)
	}
}

// ---- one snapshot for scope and rows ----

func TestJoinedRereadsGrantsBesideTheRows(t *testing.T) {
	f := newJoinedFixture(t, joinedResolvers())
	centre := "a"
	calls := 0
	f.db.respond = func(_ context.Context, op string, args map[string]any) (any, error) {
		if args["collection"] == "profiles" {
			calls++
			// The grant changes after scope selection, before the rows are read.
			if calls == 2 {
				centre = "b"
			}
			return map[string]any{"records": []any{map[string]any{"centre_id": centre}}, "hasMore": false, "nextCursor": fmt.Sprint(calls)}, nil
		}
		if op == joinedRelatedRead {
			return map[string]any{"records": []any{}, "hasMore": false, "total": "0"}, nil
		}
		return emptyAnswer(op), nil
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	f.db.forget()
	f.mustRead(centreScoped(), `{qualityReportsPage{id}}`, nil)
	// The rows finally read use the new scope, in the batch that re-read the grant.
	batches := f.db.sent()
	last := batches[len(batches)-1]
	if op, _ := last[0]["op"].(string); op != joinedRelatedRead {
		t.Fatalf("last batch: %v", last)
	}
	sameJSON(t, "scope of the rows", last[0]["args"].(map[string]any)["where"], `{"and":[{"field":"centre_id","op":"in","value":["b"]}]}`)
	readsProfiles := false
	for _, operation := range last[1:] {
		readsProfiles = readsProfiles || operation["args"].(map[string]any)["collection"] == "profiles"
	}
	if !readsProfiles {
		t.Fatalf("the grant was not re-read beside the rows: %v", last)
	}
	// A cursor that differs between two reads of the same grant is not a change.
	if !sameAccessRead(map[string]any{"records": []any{"x"}, "nextCursor": "1"}, map[string]any{"records": []any{"x"}, "nextCursor": "2"}) {
		t.Error("cursor counted as a grant change")
	}

	// Grants that never settle fail the request rather than return unconfirmed rows.
	seq := 0
	f.db.respond = func(_ context.Context, op string, args map[string]any) (any, error) {
		if args["collection"] == "profiles" {
			seq++
			return map[string]any{"records": []any{map[string]any{"centre_id": fmt.Sprint("c", seq)}}, "hasMore": false}, nil
		}
		return emptyAnswer(op), nil
	}
	if _, err := f.read(centreScoped(), `{qualityReportsPage{id}}`, nil); err == nil || !strings.Contains(err.Error(), "access kept changing") {
		t.Errorf("unstable grants: %v", err)
	}
}

// Older plans keep their batches as they were.
func TestPlansWithoutJoinedReadsSendTheSameBatches(t *testing.T) {
	plan := decodeConfig(t, reportPipeline)
	plan["graphql_args"] = map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-02-01T00:00:00Z"}
	a, p := pipelineApp(t, func(operations []any) (any, error) {
		out := []any{}
		for _, raw := range operations {
			operation := raw.(map[string]any)
			args := operation["args"].(map[string]any)
			if args["collection"] == "members" {
				out = append(out, map[string]any{"records": []any{map[string]any{"team_id": "t"}}, "hasMore": false})
				continue
			}
			out = append(out, map[string]any{"rows": []any{}, "truncated": false})
		}
		return map[string]any{"results": out}, nil
	})
	if _, err := a.callDatabasePipeline(pipelineIdentity("7", "reports:read"), plan); err != nil {
		t.Fatal(err)
	}
	if len(p.batches) != 2 || len(p.batches[1]["operations"].([]any)) != 4 {
		t.Fatalf("batches: %v", p.batches)
	}
}

// ---- dependencies ----

func TestJoinedDependenciesCoverEverythingAPlanReads(t *testing.T) {
	plan, err := validateDatabasePipeline(decodeConfig(t, qualityListPlan()))
	if err != nil {
		t.Fatal(err)
	}
	want := "[call_links calls centres grants offers profiles prospects reports sale_links sales]"
	if got := fmt.Sprint(plan.Collections()); got != want {
		t.Errorf("list:\n got %s\nwant %s", got, want)
	}
	if got := fmt.Sprint(plan.AccessCollections()); got != "[centres grants profiles]" {
		t.Errorf("authority: %s", got)
	}
	branched, err := validateDatabasePipeline(decodeConfig(t, `{"database": "crm", "steps": [{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "select": ["root.id"], "joins": [{"as": "b", "collection": "b", "on": [{"local": "root.id", "foreign": "a"}],
	     "where": {"exists": {"collection": "c", "on": [{"localField": "x", "foreignField": "y"}]}}}]},
	  {"collection": "d", "select": ["root.id"], "where": {"exists": {"collection": "e", "on": [{"localField": "x", "foreignField": "y"}]}}}],
	  "metrics": [{"name": "n", "op": "count"}]}}], "result": "$steps.s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(branched.Collections()); got != "[a b c d e]" {
		t.Errorf("branches: %s", got)
	}
	// watch_collections can add to a pipeline's dependencies but not hide one.
	config := decodeConfig(t, qualityListPlan())
	config["watch_collections"] = []any{"reports", "published_facts"}
	source := sourceRecord{Kind: "database", Config: map[string]any{"database": "crm"}}
	spec, ok := databaseWatchFor("p1", source, resolverRecord{Operation: databasePipelineOperation, Config: config})
	if !ok || fmt.Sprint(spec.collections) != "[call_links calls centres grants offers profiles prospects published_facts reports sale_links sales]" {
		t.Errorf("watch: %v %v", spec.collections, ok)
	}
	if fmt.Sprint(spec.authority) != "[centres grants profiles]" {
		t.Errorf("watch authority: %v", spec.authority)
	}
}

func TestSubscriptionWithTooManyDependenciesIsRefusedAtPublish(t *testing.T) {
	joins := []string{}
	for i := range 17 {
		joins = append(joins, fmt.Sprintf(`{"as": "j%d", "collection": "c%d", "on": [{"local": "root.id", "foreign": "x"}]}`, i, i))
	}
	plan := `{"database": "crm", "steps": [{"id": "s", "op": "joined_page", "args": {"collection": "a", "select": ["root.id"], "joins": [` + strings.Join(joins, ",") + `]}}], "result": "$steps.s"}`
	f := newJoinedFixture(t, nil)
	db := f.app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query {a: Int} type Subscription {live: [Row!]!} type Row {id: ID}`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	f.schema = schema.Version
	if _, err := upsertResolverForAPI(db, "p1", "default", "Subscription", "live", databasePipelineOperation, f.source, decodeConfig(t, plan)); err != nil {
		t.Fatal(err)
	}
	if err := f.publish(); err == nil || !strings.Contains(err.Error(), "watches at most 16") {
		t.Fatalf("publish: %v", err)
	}
}
