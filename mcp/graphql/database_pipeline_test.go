package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// dbPipelinePlatform answers db_batch with a scripted function and records every
// batch it receives.
type dbPipelinePlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu      sync.Mutex
	batches []map[string]any
	respond func(operations []any) (any, error)
}

func (p *dbPipelinePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.CallAppResultContext(context.Background(), app, tool, input, out)
}

func (p *dbPipelinePlatform) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	if app != "database" || tool != "db_batch" {
		return fmt.Errorf("unexpected call %s.%s", app, tool)
	}
	raw, _ := json.Marshal(input)
	var copied map[string]any
	_ = json.Unmarshal(raw, &copied)
	p.mu.Lock()
	p.batches = append(p.batches, copied)
	p.mu.Unlock()
	result, err := p.respond(copied["operations"].([]any))
	if err != nil {
		return err
	}
	raw, _ = json.Marshal(result)
	return json.Unmarshal(raw, out)
}

func pipelineApp(t *testing.T, respond func([]any) (any, error)) (*App, *dbPipelinePlatform) {
	t.Helper()
	p := &dbPipelinePlatform{respond: respond}
	return &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))}, p
}

func pipelineIdentity(subject string, permissions ...string) context.Context {
	return context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: subject, Permissions: permissions})
}

func decodeConfig(t *testing.T, text string) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

// A small report: per-agent calls and sales joined with a total row, the
// caller's teams resolved from their membership record.
const reportPipeline = `{
  "database": "ops",
  "params": {
    "from": {"from": "$args.from", "type": "datetime", "required": true},
    "to": {"from": "$args.to", "type": "datetime", "required": true},
    "teams": {"from": "$args.teamIds", "type": "string_list"},
    "user": {"from": "$identity.subject", "type": "string", "required": true}
  },
  "access": {
    "require_any_permission": ["reports:read"],
    "values": {
      "teams": {
        "requested": "$param.teams",
        "unrestricted_if_permission": "reports:all_teams",
        "default": {"collection": "teams", "field": "id", "where": {"field": "active", "op": "eq", "value": true}},
        "otherwise": {"collection": "members", "field": "team_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}},
        "single": true
      }
    }
  },
  "steps": [
    {"id": "calls", "op": "aggregate", "max_rows": 50, "args": {
      "collection": "calls",
      "where": {"and": [
        {"field": "team_id", "op": "in", "value": "$scope.teams"},
        {"field": "started_at", "op": "gte", "value": "$param.from"},
        {"field": "started_at", "op": "lt", "value": "$param.to"}
      ]},
      "lookups": [{"as": "agent", "collection": "agents", "localField": "agent_id"}],
      "groupBy": ["agent_id"],
      "metrics": [
        {"name": "callers", "op": "count_distinct", "field": "caller"},
        {"name": "answered", "op": "count_distinct", "field": "caller", "where": {"field": "answered", "op": "eq", "value": true}}
      ]
    }},
    {"id": "calls_total", "op": "aggregate", "args": {
      "collection": "calls",
      "where": {"field": "team_id", "op": "in", "value": "$scope.teams"},
      "metrics": [
        {"name": "callers", "op": "count_distinct", "field": "caller"},
        {"name": "answered", "op": "count_distinct", "field": "caller", "where": {"field": "answered", "op": "eq", "value": true}}
      ]
    }},
    {"id": "sales", "op": "aggregate", "max_rows": 50, "args": {
      "collection": "sales",
      "where": {"field": "team_id", "op": "in", "value": "$scope.teams"},
      "groupBy": ["agent_id"],
      "metrics": [{"name": "sales", "op": "count"}]
    }},
    {"id": "sales_total", "op": "aggregate", "args": {
      "collection": "sales", "where": {"field": "team_id", "op": "in", "value": "$scope.teams"},
      "metrics": [{"name": "sales", "op": "count"}]
    }}
  ],
  "combine": {
    "byAgent": {
      "join": ["calls", "sales"], "on": ["agent_id"], "missing": "zero",
      "total": {"key": "__total__", "steps": {"calls": "calls_total", "sales": "sales_total"}},
      "fields": {
        "callers": "calls.callers",
        "unanswered": {"op": "subtract", "args": ["calls.callers", "calls.answered"]},
        "sales": "sales.sales",
        "conversion": {"op": "safe_divide", "args": ["sales.sales", "calls.answered"]}
      }
    }
  },
  "result": {"agents": "$combine.byAgent", "teams": "$scope.teams", "totals": "$steps.calls_total.first"}
}`

func reportConfig(t *testing.T, args map[string]any) map[string]any {
	config := decodeConfig(t, reportPipeline)
	config["_project_id"] = "p1"
	config["graphql_args"] = args
	return config
}

func reportResponder(t *testing.T) func([]any) (any, error) {
	return func(operations []any) (any, error) {
		first := operations[0].(map[string]any)["args"].(map[string]any)
		if first["collection"] == "members" {
			return map[string]any{"results": []any{map[string]any{"records": []any{map[string]any{"team_id": "t1"}}, "hasMore": false}}}, nil
		}
		return map[string]any{"results": []any{
			map[string]any{"rows": []any{
				map[string]any{"agent_id": "a1", "callers": "4", "answered": "3"},
				map[string]any{"agent_id": "a2", "callers": "2", "answered": "0"},
			}, "truncated": false},
			map[string]any{"rows": []any{map[string]any{"callers": "5", "answered": "3"}}, "truncated": false},
			map[string]any{"rows": []any{map[string]any{"agent_id": "a1", "sales": "2"}, map[string]any{"agent_id": "a3", "sales": "1"}}, "truncated": false},
			map[string]any{"rows": []any{map[string]any{"sales": "3"}}, "truncated": false},
		}}, nil
	}
}

func TestDatabasePipelineRunsStepsInOneSnapshotAndCombines(t *testing.T) {
	a, p := pipelineApp(t, reportResponder(t))
	ctx := pipelineIdentity("u1", "reports:read")
	value, err := a.callDatabasePipeline(ctx, reportConfig(t, map[string]any{"from": "2026-10-01T00:00:00+02:00", "to": "2026-11-01T00:00:00+01:00"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.batches) != 2 {
		t.Fatalf("want an access batch and one step batch, got %d", len(p.batches))
	}
	access := p.batches[0]["operations"].([]any)[0].(map[string]any)["args"].(map[string]any)
	if fmt.Sprint(access["where"]) != "map[field:user_id op:eq value:u1]" || access["limit"] != float64(1000) {
		t.Fatalf("membership read: %v", access)
	}
	steps := p.batches[1]
	if steps["database"] != "ops" || steps["scope"] != "project" || steps["timeoutMs"] == nil || len(steps["operations"].([]any)) != 4 {
		t.Fatalf("step batch: %v", steps)
	}
	calls := steps["operations"].([]any)[0].(map[string]any)
	args := calls["args"].(map[string]any)
	where := args["where"].(map[string]any)["and"].([]any)
	if calls["op"] != "aggregate" || args["limit"] != float64(50) ||
		fmt.Sprint(where[0]) != "map[field:team_id op:in value:[t1]]" ||
		fmt.Sprint(where[1]) != "map[field:started_at op:gte value:2026-09-30T22:00:00Z]" {
		t.Fatalf("calls step: %v", calls)
	}
	out := value.(map[string]any)
	if !reflect.DeepEqual(out["teams"], []string{"t1"}) {
		t.Fatalf("scope: %v", out["teams"])
	}
	if got := out["totals"].(map[string]any)["callers"]; got != int64(5) {
		t.Fatalf("first row / integer conversion: %#v", got)
	}
	agents := out["agents"].([]any)
	want := []map[string]any{
		{"agent_id": "a1", "callers": int64(4), "unanswered": int64(1), "sales": int64(2), "conversion": 2.0 / 3.0},
		{"agent_id": "a2", "callers": int64(2), "unanswered": int64(2), "sales": int64(0), "conversion": nil},
		{"agent_id": "a3", "callers": int64(0), "unanswered": int64(0), "sales": int64(1), "conversion": nil},
		{"agent_id": "__total__", "callers": int64(5), "unanswered": int64(2), "sales": int64(3), "conversion": int64(1)},
	}
	if len(agents) != len(want) {
		t.Fatalf("rows: %v", agents)
	}
	for i, row := range agents {
		if !reflect.DeepEqual(row, want[i]) {
			t.Fatalf("row %d = %#v, want %#v", i, row, want[i])
		}
	}
}

func pipelineErrorCode(err error) string {
	var coded *graphqlError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestDatabasePipelineAccess(t *testing.T) {
	args := map[string]any{"from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z"}
	a, p := pipelineApp(t, reportResponder(t))
	if _, err := a.callDatabasePipeline(context.Background(), reportConfig(t, args)); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err := a.callDatabasePipeline(pipelineIdentity("u1", "other"), reportConfig(t, args)); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("missing permission: %v", err)
	}
	if len(p.batches) != 0 {
		t.Fatal("refused callers must not reach Database")
	}
	outside := map[string]any{"from": args["from"], "to": args["to"], "teamIds": []any{"t1", "t9"}}
	if _, err := a.callDatabasePipeline(pipelineIdentity("u1", "reports:read"), reportConfig(t, outside)); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("requested team outside membership: %v", err)
	}
	if len(p.batches) != 1 {
		t.Fatalf("an out-of-scope request must stop after the access read, got %d batches", len(p.batches))
	}

	// Unrestricted callers use exactly what they requested, without reads.
	p.batches = nil
	chosen := map[string]any{"from": args["from"], "to": args["to"], "teamIds": []any{" t7 ", "t8", "t7"}}
	value, err := a.callDatabasePipeline(pipelineIdentity("u1", "reports:read", "reports:all_teams"), reportConfig(t, chosen))
	if err != nil || len(p.batches) != 1 || !reflect.DeepEqual(value.(map[string]any)["teams"], []string{"t7", "t8"}) {
		t.Fatalf("unrestricted: %v %d %v", err, len(p.batches), value)
	}

	// An unrestricted caller requesting nothing gets the default read.
	p.batches = nil
	p.respond = func(operations []any) (any, error) {
		if operations[0].(map[string]any)["args"].(map[string]any)["collection"] == "teams" {
			return map[string]any{"results": []any{map[string]any{"records": []any{map[string]any{"id": "t1"}, map[string]any{"id": "t2"}}}}}, nil
		}
		return reportResponder(t)(operations)
	}
	value, err = a.callDatabasePipeline(pipelineIdentity("u1", "reports:read", "reports:all_teams"), reportConfig(t, args))
	if err != nil || !reflect.DeepEqual(value.(map[string]any)["teams"], []string{"t1", "t2"}) {
		t.Fatalf("default: %v %v", err, value)
	}

	// A restricted caller must resolve to exactly one team.
	p.respond = func(operations []any) (any, error) {
		return map[string]any{"results": []any{map[string]any{"records": []any{map[string]any{"team_id": "t1"}, map[string]any{"team_id": "t2"}}}}}, nil
	}
	if _, err := a.callDatabasePipeline(pipelineIdentity("u1", "reports:read"), reportConfig(t, args)); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("ambiguous membership: %v", err)
	}
}

func TestDatabasePipelineNeverReturnsPartialResults(t *testing.T) {
	args := map[string]any{"from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z"}
	ctx := pipelineIdentity("u1", "reports:read", "reports:all_teams")
	args["teamIds"] = []any{"t1"}
	cases := map[string]struct {
		respond func([]any) (any, error)
		code    string
	}{
		"truncated aggregate": {func(ops []any) (any, error) {
			results := []any{}
			for range ops {
				results = append(results, map[string]any{"rows": []any{}, "truncated": true})
			}
			return map[string]any{"results": results}, nil
		}, "source_result_truncated"},
		"resource limit": {func([]any) (any, error) {
			return nil, errors.New("resource_limit: read batch response exceeds 4 MiB")
		}, "resource_limit"},
		"timeout": {func([]any) (any, error) {
			return nil, errors.New("database.db_batch: context deadline exceeded")
		}, "timeout"},
		"missing result": {func([]any) (any, error) {
			return map[string]any{"results": []any{}}, nil
		}, "source_error"},
	}
	for name, c := range cases {
		a, _ := pipelineApp(t, c.respond)
		value, err := a.callDatabasePipeline(ctx, reportConfig(t, args))
		if value != nil || pipelineErrorCode(err) != c.code {
			t.Fatalf("%s: value=%v err=%v", name, value, err)
		}
	}

	// A truncated find step also fails.
	config := decodeConfig(t, `{"steps":[{"id":"latest","op":"find","max_rows":2,"args":{"collection":"calls"}}],"result":{"latest":"$steps.latest"}}`)
	config["_project_id"] = "p1"
	a, _ := pipelineApp(t, func([]any) (any, error) {
		return map[string]any{"results": []any{map[string]any{"records": []any{map[string]any{}, map[string]any{}}, "hasMore": true}}}, nil
	})
	if _, err := a.callDatabasePipeline(context.Background(), config); pipelineErrorCode(err) != "source_result_truncated" {
		t.Fatalf("find hasMore: %v", err)
	}
}

func TestDatabasePipelineValidation(t *testing.T) {
	base := `{"params":{"p":{"from":"$args.p"}},"steps":[%s],"result":{"r":"$steps.s"}}`
	cases := map[string]string{
		"client-chosen field":   `{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["$param.p"],"metrics":[{"name":"n","op":"count"}]}}`,
		"client-chosen metric":  `{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"sum","field":"$param.p"}]}}`,
		"client collection":     `{"id":"s","op":"count","args":{"collection":"$param.p"}}`,
		"filter field":          `{"id":"s","op":"count","args":{"collection":"c","where":{"field":"$param.p","op":"eq","value":1}}}`,
		"unknown param":         `{"id":"s","op":"count","args":{"collection":"c","where":{"field":"f","op":"eq","value":"$param.nope"}}}`,
		"unknown scope":         `{"id":"s","op":"count","args":{"collection":"c","where":{"field":"f","op":"in","value":"$scope.teams"}}}`,
		"write op":              `{"id":"s","op":"delete","args":{"collection":"c"}}`,
		"client limit":          `{"id":"s","op":"find","args":{"collection":"c","limit":5}}`,
		"too many rows":         `{"id":"s","op":"find","max_rows":5000,"args":{"collection":"c"}}`,
		"aggregate w/o metrics": `{"id":"s","op":"aggregate","args":{"collection":"c"}}`,
	}
	for name, step := range cases {
		if _, err := validateDatabasePipeline(decodeConfig(t, fmt.Sprintf(base, step))); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	good := `{"id":"s","op":"aggregate","args":{"collection":"c","where":{"field":"f","op":"eq","value":"$param.p"},"metrics":[{"name":"n","op":"count","where":{"field":"g","op":"eq","value":"$param.p"}}]}}`
	if _, err := validateDatabasePipeline(decodeConfig(t, fmt.Sprintf(base, good))); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	for name, text := range map[string]string{
		"unknown result":  `{"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":{"r":"$steps.x"}}`,
		"combine bad ref": `{"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["k"],"metrics":[{"name":"n","op":"count"}]}}],"combine":{"j":{"join":["s"],"on":["k"],"fields":{"x":"other.n"}}},"result":{"r":"$combine.j"}}`,
		"combine bad op":  `{"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["k"],"metrics":[{"name":"n","op":"count"}]}}],"combine":{"j":{"join":["s"],"on":["k"],"fields":{"x":{"op":"call","module":"m","version":1}}}},"result":{"r":"$combine.j"}}`,
		"grouped total":   `{"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["k"],"metrics":[{"name":"n","op":"count"}]}}],"combine":{"j":{"join":["s"],"on":["k"],"total":{"key":"all","steps":{"s":"s"}}}},"result":{"r":"$combine.j"}}`,
		"unknown key":     `{"sql":"SELECT 1","steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":{"r":"$steps.s"}}`,
		"no steps":        `{"steps":[],"result":{"r":"$steps.s"}}`,
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestDatabasePipelineWatchesEverythingItReads(t *testing.T) {
	config := decodeConfig(t, reportPipeline)
	source := sourceRecord{Kind: "database", Config: map[string]any{"database": "ops"}}
	spec, ok := databaseWatchFor("p1", source, resolverRecord{Operation: databasePipelineOperation, Config: config})
	if !ok || strings.Join(spec.collections, ",") != "agents,calls,members,sales,teams" || spec.database != "ops" {
		t.Fatalf("watch: %#v %v", spec, ok)
	}
}

func TestSafeDivide(t *testing.T) {
	runtime := moduleRuntime{}
	for _, c := range []struct {
		args []any
		want any
	}{{[]any{"3", "0"}, nil}, {[]any{nil, "2"}, nil}, {[]any{"3", "2"}, 1.5}, {[]any{"4", "2"}, int64(2)}} {
		args := []any{}
		for _, v := range c.args {
			args = append(args, map[string]any{"const": v})
		}
		got, err := runtime.eval(map[string]any{"op": "safe_divide", "args": args}, nil, 0)
		if err != nil || got != c.want {
			t.Fatalf("safe_divide%v = %#v, %v", c.args, got, err)
		}
	}
}
