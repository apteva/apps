package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDatabasePipelineDistinctAccessValidation(t *testing.T) {
	plan := func(value string) string {
		return `{"params":{"user":{"from":"$identity.subject"}},"access":{"values":{"v":` + value + `}},"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":"$steps.s"}`
	}
	for name, value := range map[string]string{
		"string":        `{"otherwise":{"collection":"r","field":"c","distinct":"yes"}}`,
		"orderBy":       `{"otherwise":{"collection":"r","field":"c","distinct":true,"orderBy":[{"field":"id"}]}}`,
		"limit":         `{"otherwise":{"collection":"r","field":"c","distinct":true,"orderBy":[{"field":"id"}],"limit":1}}`,
		"single_record": `{"single_record":true,"otherwise":{"collection":"r","field":"c","distinct":true}}`,
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, plan(value))); err == nil {
			t.Errorf("distinct with %s was accepted", name)
		}
	}
	for _, value := range []string{
		`{"otherwise":{"collection":"r","field":"c","distinct":true,"max":5}}`,
		`{"single":true,"otherwise":{"collection":"r","field":"c","distinct":true}}`,
		`{"otherwise":{"collection":"r","field":"c","distinct":false,"orderBy":[{"field":"id"}],"limit":1}}`,
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, plan(value))); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
}

func TestDatabasePipelineDistinctAccessReads(t *testing.T) {
	config := func(max int, extra string) map[string]any {
		c := decodeConfig(t, fmt.Sprintf(`{"params":{"user":{"from":"$identity.subject"},"want":{"from":"$args.centres","type":"string_list"}},
		  "access":{"values":{"centres":{"optional":true,%s"otherwise":{"collection":"sales","field":"centre_id","distinct":true,"max":%d,
		    "where":{"field":"seller","op":"eq","value":"$param.user"}}}}},
		  "steps":[{"id":"s","op":"find","args":{"collection":"centres","where":{"field":"id","op":"in","value":"$scope.centres"},"orderBy":[{"field":"id"}]}}],
		  "result":"$steps.s"}`, extra, max))
		c["_project_id"] = "p1"
		return c
	}
	// groups answers the access aggregate with n distinct centres (each
	// standing for many sales) and echoes the step's in-list as records.
	groups := func(n int, page map[string]any) func([]any) (any, error) {
		return func(operations []any) (any, error) {
			out := []any{}
			for _, raw := range operations {
				op := raw.(map[string]any)
				args := op["args"].(map[string]any)
				if op["op"] == "aggregate" {
					if page != nil {
						out = append(out, page)
						continue
					}
					rows := []any{}
					for i := 0; i < n; i++ {
						rows = append(rows, map[string]any{"centre_id": fmt.Sprintf("c%03d", i), "access_count": "5000"})
					}
					out = append(out, map[string]any{"rows": rows, "truncated": false})
					continue
				}
				records := []any{}
				for _, id := range asAnyList(args["where"].(map[string]any)["value"]) {
					records = append(records, map[string]any{"id": id})
				}
				out = append(out, map[string]any{"records": records, "hasMore": false})
			}
			return map[string]any{"results": out}, nil
		}
	}
	run := func(c map[string]any, respond func([]any) (any, error), args map[string]any) ([]any, *dbPipelinePlatform, error) {
		a, p := pipelineApp(t, respond)
		identity := pipelineIdentity("u1")
		for k, v := range args {
			c["graphql_args"] = map[string]any{k: v}
		}
		out, err := a.callDatabasePipeline(identity, c)
		list, _ := out.([]any)
		return list, p, err
	}

	// One centre with thousands of sales is one value. The read is an
	// aggregate grouped on the configured field, bounded at max+1 groups,
	// with the caller's filter kept and null groups excluded.
	out, p, err := run(config(999, ""), groups(1, nil), nil)
	if err != nil || len(out) != 1 {
		t.Fatalf("repeated sales in one centre: %v %v", out, err)
	}
	op := p.batches[0]["operations"].([]any)[0].(map[string]any)
	args := op["args"].(map[string]any)
	if op["op"] != "aggregate" || fmt.Sprint(args["groupBy"]) != "[centre_id]" || fmt.Sprint(args["limit"]) != "1000" || args["select"] != nil {
		t.Fatalf("distinct read: %v", op)
	}
	if where := fmt.Sprint(args["where"]); !strings.Contains(where, "seller op:eq value:u1") || !strings.Contains(where, "centre_id op:is_not_null") {
		t.Fatalf("distinct filter: %s", where)
	}

	// Exactly max groups pass; max+1 fail closed.
	if out, _, err := run(config(3, ""), groups(3, nil), nil); err != nil || len(out) != 3 {
		t.Fatalf("max groups: %v %v", out, err)
	}
	for name, c := range map[string]struct {
		respond func([]any) (any, error)
		code    string
	}{
		"max+1 groups": {groups(4, nil), "source_result_truncated"},
		"truncated":    {groups(0, map[string]any{"rows": []any{}, "truncated": true}), "source_result_truncated"},
		"find-shaped":  {groups(0, map[string]any{"records": []any{}, "hasMore": false}), "source_error"},
		"no result":    {func([]any) (any, error) { return map[string]any{"results": []any{}}, nil }, "source_error"},
		"database": {func([]any) (any, error) {
			return nil, errors.New("database.db_batch: invalid_argument: grouping fields must be distinct scalars (code=-32000)")
		}, "invalid_argument"},
	} {
		if _, _, err := run(config(3, ""), c.respond, nil); pipelineErrorCode(err) != c.code {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}

	// Client requests narrow within the distinct set and never widen it.
	requested := `"requested":"$param.want",`
	if out, _, err := run(config(999, requested), groups(3, nil), map[string]any{"centres": []any{"c001"}}); err != nil || fmt.Sprint(out) != "[map[id:c001]]" {
		t.Fatalf("requested inside: %v %v", out, err)
	}
	if _, _, err := run(config(999, requested), groups(3, nil), map[string]any{"centres": []any{"c999"}}); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("requested outside: %v", err)
	}
	// No sales: an optional empty scope, nothing read.
	if out, p, err := run(config(999, ""), groups(0, nil), nil); err != nil || len(out) != 0 || len(p.batches) != 1 {
		t.Fatalf("no groups: %v %v", out, err)
	}
}

func TestDatabasePipelineDistinctMetricName(t *testing.T) {
	if distinctAccessMetric("centre_id") != "access_count" || distinctAccessMetric("access_count") != "access_count_n" {
		t.Fatal("metric name collides with the grouped field")
	}
}
