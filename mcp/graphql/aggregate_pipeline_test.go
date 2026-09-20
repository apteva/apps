package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

func pipelineSources() []sourceRecord {
	return []sourceRecord{
		{ID: 1, Name: "prospects", Kind: "tables", Status: "active", Config: map[string]any{"table": "prospects"}},
		{ID: 2, Name: "calls", Kind: "tables", Status: "active", Config: map[string]any{"table": "calls"}},
		{ID: 3, Name: "sales", Kind: "tables", Status: "active", Config: map[string]any{"table": "sales"}},
	}
}

func pipelineConfig() map[string]any {
	return map[string]any{
		"version": 1, "engine": "tables_query", "sources": []any{"prospects", "calls", "sales"},
		"sql": `WITH latest AS (SELECT prospect_id, status, ROW_NUMBER() OVER (PARTITION BY prospect_id ORDER BY created_at DESC) AS rn FROM {calls}) SELECT p.centre_id, COUNT(*) AS total, SUM(CASE WHEN l.status = 'won' THEN 1 ELSE 0 END) AS won FROM {prospects} p LEFT JOIN latest l ON l.prospect_id=p.id AND l.rn=1 LEFT JOIN {sales} s ON s.prospect_id=p.id WHERE p.centre_id=? AND p.created_at>=? GROUP BY p.centre_id`,
		"params": []any{
			map[string]any{"from": "$identity.claim.centre_id", "type": "string", "required": true},
			map[string]any{"from": "$args.periodStart", "type": "datetime", "required": true},
		},
		"result": "single", "max_rows": 1,
	}
}

func TestAggregatePipelineValidationIsResolverOwned(t *testing.T) {
	sources := pipelineSources()
	plan, err := validateAggregatePipeline(aggregatePipelineOperation, pipelineConfig(), sources, 1)
	if err != nil || plan.Result != "single" || plan.MaxRows != 1 || len(plan.Tables) != 3 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	tests := []struct {
		name string
		edit func(map[string]any, []sourceRecord) []sourceRecord
	}{
		{"missing SQL", func(c map[string]any, s []sourceRecord) []sourceRecord { delete(c, "sql"); return s }},
		{"client SQL mapping", func(c map[string]any, s []sourceRecord) []sourceRecord {
			c["sql"] = map[string]any{"from": "$args.sql"}
			return s
		}},
		{"unknown source", func(c map[string]any, s []sourceRecord) []sourceRecord {
			c["sources"] = []any{"prospects", "missing"}
			return s
		}},
		{"non Tables source", func(c map[string]any, s []sourceRecord) []sourceRecord { s[1].Kind = "http"; return s }},
		{"undeclared placeholder", func(c map[string]any, s []sourceRecord) []sourceRecord { c["sources"] = []any{"prospects"}; return s }},
		{"scoped source", func(c map[string]any, s []sourceRecord) []sourceRecord { s[0].Config["where"] = []any{}; return s }},
		{"bound source omitted", func(c map[string]any, s []sourceRecord) []sourceRecord {
			c["sources"] = []any{"calls", "sales"}
			c["sql"] = "SELECT * FROM {calls}"
			return s
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var config map[string]any
			encoded, _ := json.Marshal(pipelineConfig())
			_ = json.Unmarshal(encoded, &config)
			var cloned []sourceRecord
			encoded, _ = json.Marshal(sources)
			_ = json.Unmarshal(encoded, &cloned)
			cloned = test.edit(config, cloned)
			if _, err := validateAggregatePipeline(aggregatePipelineOperation, config, cloned, 1); err == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
}

func TestAggregatePipelineParametersUseOnlyTrustedContexts(t *testing.T) {
	plan, err := validateAggregatePipeline(aggregatePipelineOperation, pipelineConfig(), pipelineSources(), 1)
	if err != nil {
		t.Fatal(err)
	}
	identity := &requestIdentity{Subject: "user-1", Tenant: "tenant-1", Claims: map[string]any{"centre_id": "centre-7"}}
	ctx := context.WithValue(context.Background(), identityKey{}, identity)
	input, err := buildAggregatePipelineInput(ctx, plan, map[string]any{"periodStart": "2026-09-01T02:00:00+02:00"}, nil, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if input["sql"] != plan.SQL || !reflect.DeepEqual(input["params"], []any{"centre-7", "2026-09-01T00:00:00Z"}) {
		t.Fatalf("input=%#v", input)
	}
	if _, err := buildAggregatePipelineInput(context.Background(), plan, map[string]any{"periodStart": "2026-09-01T00:00:00Z", "centre_id": "spoof"}, nil, "p1"); err == nil {
		t.Fatal("unverified client value replaced identity claim")
	}

	parentPlan, err := validateAggregatePipeline(aggregatePipelineOperation, map[string]any{
		"version": 1, "engine": "tables_query", "sources": []any{"calls"}, "sql": "SELECT COUNT(*) n FROM {calls} WHERE prospect_id=?", "params": []any{map[string]any{"from": "$parent.business.id", "type": "number"}}, "result": "single", "max_rows": 1,
	}, pipelineSources(), 2)
	if err != nil {
		t.Fatal(err)
	}
	input, err = buildAggregatePipelineInput(ctx, parentPlan, nil, map[string]any{"business": map[string]any{"id": "42"}}, "p1")
	if err != nil || !reflect.DeepEqual(input["params"], []any{float64(42)}) {
		t.Fatalf("nested parent input=%#v err=%v", input, err)
	}
}

func TestAggregatePipelineResultModesAndLimits(t *testing.T) {
	base := &aggregatePipelinePlan{MaxRows: 2, OnTruncated: "error"}
	envelope := map[string]any{"columns": []any{"n"}, "rows": []any{map[string]any{"n": 1}}, "truncated": false}
	for _, mode := range []string{"rows", "single", "envelope"} {
		plan := *base
		plan.Result = mode
		value, err := transformAggregatePipelineResult(&plan, envelope)
		if err != nil || value == nil {
			t.Fatalf("mode=%s value=%#v err=%v", mode, value, err)
		}
	}
	plan := *base
	plan.Result = "single"
	if _, err := transformAggregatePipelineResult(&plan, map[string]any{"rows": []any{map[string]any{}, map[string]any{}}, "truncated": false}); err == nil {
		t.Fatal("single accepted multiple rows")
	}
	plan.Result = "rows"
	if _, err := transformAggregatePipelineResult(&plan, map[string]any{"rows": []any{map[string]any{}, map[string]any{}, map[string]any{}}, "truncated": false}); errorCode(err) != "row_limit_exceeded" {
		t.Fatalf("max rows error=%v", err)
	}
	if _, err := transformAggregatePipelineResult(&plan, map[string]any{"rows": []any{}, "truncated": true}); errorCode(err) != "source_result_truncated" {
		t.Fatalf("truncation error=%v", err)
	}
}

type pipelinePlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu     sync.Mutex
	calls  []string
	inputs []map[string]any
}

func (p *pipelinePlatform) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	return p.CallAppResult(app, tool, input, out)
}

func (p *pipelinePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, tool)
	p.inputs = append(p.inputs, input)
	if app != "tables" {
		return fmt.Errorf("unexpected app %s", app)
	}
	queryResult := func(args map[string]any) map[string]any {
		return map[string]any{"columns": []any{"total"}, "rows": []any{map[string]any{"total": len(args["params"].([]any))}}, "truncated": false}
	}
	var value any
	if tool == "tables_batch" {
		results := map[string]any{}
		for _, raw := range input["operations"].([]map[string]any) {
			results[raw["id"].(string)] = map[string]any{"status": "ok", "result": queryResult(raw["args"].(map[string]any))}
		}
		value = map[string]any{"results": results}
	} else if tool == "tables_query" {
		value = queryResult(input)
	} else {
		return fmt.Errorf("unexpected tool %s", tool)
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, out)
}

func TestAggregatePipelineStandardGraphQLAndSiblingBatch(t *testing.T) {
	platform := &pipelinePlatform{}
	sources := pipelineSources()
	sourceMap := map[int64]sourceRecord{}
	for _, source := range sources {
		sourceMap[source.ID] = source
	}
	config := map[string]any{
		"version": 1, "engine": "tables_query", "sources": []any{"prospects"}, "sql": "SELECT COUNT(*) AS total FROM {prospects} WHERE centre_id=?", "params": []any{map[string]any{"from": "$args.centre", "type": "string"}}, "result": "single", "max_rows": 1,
	}
	bindings := &executionBindings{sources: sourceMap, resolvers: map[string]resolverRecord{
		"Query.first":  {SourceID: 1, Operation: aggregatePipelineOperation, Config: config},
		"Query.second": {SourceID: 1, Operation: aggregatePipelineOperation, Config: config},
	}}
	_, schema, ctx := standardApp(t, `type Query { first(centre: String!): Metric! second(centre: String!): Metric! } type Metric { total: Int! }`, platform, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ first(centre:"a") { total } second(centre:"b") { total } }`})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	if fmt.Sprint(platform.calls) != "[tables_batch]" {
		t.Fatalf("independent pipelines were not batched: %v", platform.calls)
	}
	operations := platform.inputs[0]["operations"].([]map[string]any)
	if len(operations) != 2 || operations[0]["operation"] != "tables_query" || strings.Contains(fmt.Sprint(operations), "graphql_args") {
		t.Fatalf("unsafe or inefficient batch: %#v", operations)
	}
}

func TestAggregatePipelineParentProjectionDependencies(t *testing.T) {
	config := map[string]any{"params": []any{
		map[string]any{"from": "$parent.centre_id", "type": "string"},
		map[string]any{"from": "$parent.owner.id", "type": "string"},
		map[string]any{"from": "$args.period", "type": "string"},
	}}
	if got := aggregatePipelineParentDependencies(config); !reflect.DeepEqual(got, []string{"centre_id", "owner"}) {
		t.Fatalf("dependencies=%v", got)
	}
}
