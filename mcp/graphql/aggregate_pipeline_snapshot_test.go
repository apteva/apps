package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func snapshotConfig() map[string]any {
	return map[string]any{"version": 3, "engine": "tables_batch", "mode": "read_snapshot", "sources": []any{"prospects"}, "final_stage": "summary", "result": "single", "max_rows": 1, "stages": []any{
		map[string]any{"id": "facts", "columns": []any{"payload"}, "sql": "SELECT json_object('n',COUNT(*)) FROM {prospects} WHERE centre_id=?", "params": []any{map[string]any{"from": "$identity.tenant", "type": "string"}}, "max_rows": 1, "max_bytes": 1024},
		map[string]any{"id": "summary", "columns": []any{"total"}, "sql": "SELECT json_extract(?,'$.n') + ?", "params": []any{map[string]any{"from": "$stage.facts.rows.0.payload", "type": "json", "required": true}, map[string]any{"from": "$args.extra", "type": "number"}}, "max_rows": 1, "max_bytes": 1024},
	}}
}

func snapshotClone() map[string]any {
	b, _ := json.Marshal(snapshotConfig())
	var c map[string]any
	_ = json.Unmarshal(b, &c)
	return c
}

// Test transport implements the Tables shape against real SQLite. Native
// sidecar tests below separately verify released Tables authorization/refs.
func runSnapshotBatch(ctx context.Context, db *sql.DB, ops []map[string]any, before func(string)) (map[string]any, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	results := map[string]any{}
	for _, op := range ops {
		id := op["id"].(string)
		if before != nil {
			before(id)
		}
		input := op["args"].(map[string]any)
		params := make([]any, 0)
		var refErr error
		for _, value := range input["params"].([]any) {
			if ref, ok := value.(map[string]any); ok {
				parts := strings.Split(ref["$ref"].(string), ".")
				var root any
				previous, ok := results[parts[0]].(map[string]any)
				if !ok || previous["status"] != "ok" {
					refErr = fmt.Errorf("dependency failed")
					break
				}
				root = previous["result"]
				for _, part := range parts[1:] {
					switch v := root.(type) {
					case map[string]any:
						root = v[part]
					case []any:
						n, e := strconv.Atoi(part)
						if e != nil || n < 0 || n >= len(v) {
							refErr = fmt.Errorf("missing ref")
						} else {
							root = v[n]
						}
					default:
						refErr = fmt.Errorf("invalid ref")
					}
					if refErr != nil {
						break
					}
				}
				if refErr != nil {
					break
				}
				value = root
			}
			params = append(params, value)
		}
		if refErr != nil {
			results[id] = map[string]any{"status": "skipped_dependency"}
			continue
		}
		rows, err := tx.QueryContext(ctx, strings.ReplaceAll(input["sql"].(string), "{prospects}", `"prospects"`), params...)
		if err != nil {
			results[id] = map[string]any{"status": "error"}
			continue
		}
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		values := []any{}
		for rows.Next() {
			dest := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range dest {
				ptrs[i] = &dest[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			row := map[string]any{}
			for i, col := range columns {
				row[col] = dest[i]
			}
			values = append(values, row)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		results[id] = map[string]any{"status": "ok", "result": map[string]any{"rows": values, "columns": columns, "truncated": false, "projections": []any{map[string]any{"name": id, "generation": 7, "secret": "hidden"}}}}
	}
	return results, nil
}

func snapshotRead(t *testing.T, c map[string]any) (any, error) {
	t.Helper()
	p := stagedPlan(t, c)
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
	input, err := buildAggregatePipelineInput(ctx, p, map[string]any{"extra": 3, "tenant": "south"}, nil, "p1")
	if err != nil {
		return nil, err
	}
	results, err := runSnapshotBatch(ctx, stagedDB(t), input["operations"].([]map[string]any), nil)
	if err != nil {
		return nil, err
	}
	out := map[string]upstreamResult{}
	for id, v := range results {
		item := v.(map[string]any)
		if item["status"] != "ok" {
			return nil, internal("backend stage failed")
		}
		out[id] = upstreamResult{Value: item["result"]}
	}
	value, err := decodeSnapshotPipeline(p, out)
	if err != nil {
		return nil, err
	}
	return transformAggregatePipelineResult(p, value)
}

func TestSnapshotPipelineBindingsFinalOnlyAndMetadata(t *testing.T) {
	c := snapshotConfig()
	stages := c["stages"].([]any)
	c["stages"] = []any{stages[1], stages[0]}
	p := stagedPlan(t, c)
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
	input, err := buildAggregatePipelineInput(ctx, p, map[string]any{"extra": 3, "tenant": "south"}, nil, "p1")
	if err != nil {
		t.Fatal(err)
	}
	ops := input["operations"].([]map[string]any)
	if len(ops) != 2 || ops[0]["id"] != "facts" || ops[1]["id"] != "summary" || input["mode"] != "read_snapshot" {
		t.Fatal(input)
	}
	if !reflect.DeepEqual(ops[1]["args"].(map[string]any)["params"].([]any)[0], map[string]any{"$ref": "facts.rows.0.payload"}) {
		t.Fatal(input)
	}
	if fmt.Sprint(ops[0]["args"].(map[string]any)["params"]) != "[north]" {
		t.Fatal(input)
	}
	if _, err := buildAggregatePipelineInput(context.Background(), p, map[string]any{"extra": 3, "tenant": "north"}, nil, "p1"); err == nil {
		t.Fatal("unverified identity accepted")
	}
	c["result"] = "envelope"
	value, err := snapshotRead(t, c)
	if err != nil {
		t.Fatal(err)
	}
	e := value.(map[string]any)
	if len(e["rows"].([]any)) != 1 || fmt.Sprint(e["rows"].([]any)[0].(map[string]any)["total"]) != "5" || strings.Contains(fmt.Sprint(e), "payload") || strings.Contains(fmt.Sprint(e), "hidden") || len(e["projections"].([]map[string]any)) != 2 {
		t.Fatal(e)
	}
}

func TestSnapshotPipelineRejectsInvalidConfiguration(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"mode": func(c map[string]any) { c["mode"] = "best_effort" },
		"relation": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT ? + ? FROM {stage:facts}"
		},
		"unknown": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.missing.rows.0.payload"
		},
		"column": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.facts.rows.0.secret"
		},
		"array": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.facts.rows.-1.payload"
		},
		"row budget": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.facts.rows.1.payload"
		},
		"cycle": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.summary.rows.0.total"
		},
		"source": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT payload FROM {sales} WHERE centre_id=?"
		},
		"sql size": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT ? /*" + strings.Repeat("x", 65536) + "*/"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := snapshotClone()
			edit(c)
			if _, err := validateAggregatePipeline(aggregatePipelineOperation, c, pipelineSources(), 1); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	c := snapshotConfig()
	c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$parent.owner.id", "type": "string"}}
	if got := aggregatePipelineParentDependencies(c); !reflect.DeepEqual(got, []string{"owner"}) {
		t.Fatal(got)
	}
}

func TestSnapshotPipelineBudgetsFailuresAndRequiredTypes(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"rows": func(c map[string]any) {
			s := c["stages"].([]any)[0].(map[string]any)
			s["sql"] = "SELECT json_object('n',id) FROM {prospects} WHERE centre_id=?"
		},
		"bytes": func(c map[string]any) { c["stages"].([]any)[0].(map[string]any)["max_bytes"] = 1 },
		"intermediate SQL failure": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT no_such_column FROM {prospects} WHERE centre_id=?"
		},
		"missing row": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT id FROM {prospects} WHERE centre_id=? AND 0"
		},
		"required null": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT NULL FROM {prospects} WHERE centre_id=? LIMIT 1"
		},
		"invalid JSON": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT 'not JSON' FROM {prospects} WHERE centre_id=? LIMIT 1"
		},
		"wrong type": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["type"] = "boolean"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := snapshotClone()
			edit(c)
			if value, err := snapshotRead(t, c); err == nil || value != nil {
				t.Fatal(value, err)
			}
		})
	}
	p := stagedPlan(t, snapshotConfig())
	if p.validateStageLimits(releaseLimits{MaxRows: 1, MaxResponseBytes: 2048}) == nil || p.validateStageLimits(releaseLimits{MaxRows: 2, MaxResponseBytes: 2047}) == nil {
		t.Fatal("cumulative budgets ignored")
	}
	out := map[string]upstreamResult{"facts": {Value: map[string]any{"rows": []any{map[string]any{"payload": "{}"}}, "truncated": true}}, "summary": {Value: map[string]any{"rows": []any{map[string]any{"total": 1}}}}}
	if _, err := decodeSnapshotPipeline(p, out); errorCode(err) != "source_result_truncated" {
		t.Fatal(err)
	}
}

func TestSnapshotPipelineCoalescingIdentityFailureAndCancellation(t *testing.T) {
	t.Run("sharing and identity isolation", func(t *testing.T) {
		unblock := make(chan struct{})
		p := &stagedPlatform{db: stagedDB(t), entered: make(chan struct{}), block: unblock}
		app := stagedReleaseFixture(t, p, snapshotConfig(), map[string]any{"coalesce_reads": true})
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "u1", Tenant: "north", Permissions: []string{"read"}, Expires: time.Now().Add(time.Hour)})
		done := make(chan executeResult, 2)
		errs := make(chan error, 2)
		run := func(query string) {
			out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: query})
			done <- out
			errs <- err
		}
		go run(`query Read {summary{total}}`)
		<-p.entered
		go run("# equivalent\nquery Read { summary {total} }")
		eventuallyRuntime(t, func() bool {
			app.runtime.mu.Lock()
			defer app.runtime.mu.Unlock()
			for _, f := range app.runtime.flights {
				if f.waiters == 2 {
					return true
				}
			}
			return false
		})
		close(unblock)
		a, b := <-done, <-done
		if <-errs != nil || <-errs != nil || len(a.Errors) > 0 || len(b.Errors) > 0 || p.calls != 1 || a.Runtime.ExecutionID != b.Runtime.ExecutionID {
			t.Fatal(a, b, p.calls)
		}
		if len(a.Extensions) == 0 || strings.Contains(fmt.Sprint(a.Extensions), "hidden") {
			t.Fatal(a.Extensions)
		}
		for _, identity := range []*requestIdentity{{Subject: "u2", Tenant: "south", Permissions: []string{"read"}, Expires: time.Now().Add(time.Hour)}, {Subject: "u1", Tenant: "north", Permissions: []string{"other"}, Expires: time.Now().Add(time.Hour)}} {
			other := context.WithValue(context.Background(), identityKey{}, identity)
			out, err := app.execute(other, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
			if err != nil || len(out.Errors) > 0 {
				t.Fatal(out, err)
			}
		}
		if p.calls != 3 {
			t.Fatal("authorization scope shared", p.calls)
		}
	})
	t.Run("failed executions not cached", func(t *testing.T) {
		c := snapshotConfig()
		c["stages"].([]any)[0].(map[string]any)["max_bytes"] = 1
		p := &stagedPlatform{db: stagedDB(t)}
		app := stagedReleaseFixture(t, p, c, map[string]any{"coalesce_reads": true})
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
		for range 2 {
			out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
			if err != nil || len(out.Errors) != 1 || out.Data["summary"] != nil {
				t.Fatal(out, err)
			}
		}
		if p.calls != 2 {
			t.Fatal(p.calls)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		p := &stagedPlatform{db: stagedDB(t), block: make(chan struct{})}
		app := stagedReleaseFixture(t, p, snapshotConfig(), map[string]any{"max_execution_ms": 30})
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
		start := time.Now()
		out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
		if time.Since(start) > time.Second || err == nil && len(out.Errors) == 0 {
			t.Fatal(out, err)
		}
	})
}

func TestSnapshotPipelineSQLiteReferenceCompilationRegression(t *testing.T) {
	legacy := stagedConfig()
	stages := []any{}
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("s%d", i)
		sql := "SELECT COUNT(*) FROM {prospects}"
		if i > 0 {
			sql = fmt.Sprintf("SELECT a.payload FROM {stage:s%d} a JOIN {stage:s%d} b ON a.payload=b.payload", i-1, i-1)
		}
		stages = append(stages, map[string]any{"id": id, "columns": []any{"payload"}, "sql": sql, "max_rows": 1, "max_bytes": 1024})
	}
	legacy["stages"], legacy["final_stage"] = stages, "s15"
	plan := stagedPlan(t, legacy)
	input, _ := buildAggregatePipelineInput(context.Background(), plan, nil, nil, "p1")
	_, err := runStagedSQL(context.Background(), stagedDB(t), input)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "references") {
		t.Fatalf("expected SQLite reference compilation limit, got %v", err)
	}
	t.Logf("combined statement: %v", err)
	separate := map[string]any{"version": 3, "engine": "tables_batch", "mode": "read_snapshot", "sources": []any{"prospects"}, "final_stage": "s15", "result": "single", "max_rows": 1, "stages": []any{}}
	for i := 0; i < 16; i++ {
		sql := "SELECT COUNT(*) FROM {prospects}"
		params := []any{}
		if i > 0 {
			sql = "SELECT ? + 0 * ?"
			for range 2 {
				params = append(params, map[string]any{"from": fmt.Sprintf("$stage.s%d.rows.0.payload", i-1), "type": "number"})
			}
		}
		separate["stages"] = append(separate["stages"].([]any), map[string]any{"id": fmt.Sprintf("s%d", i), "columns": []any{"payload"}, "sql": sql, "params": params, "max_rows": 1, "max_bytes": 1024})
	}
	value, err := snapshotRead(t, separate)
	if err != nil || fmt.Sprint(value.(map[string]any)["payload"]) != "3" {
		t.Fatal(value, err)
	}
}

func TestSnapshotPipelineMemoBudgetAndCompletionIsolation(t *testing.T) {
	platform := &stagedPlatform{db: stagedDB(t)}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	state := &standardRequest{limits: defaultReleaseLimits()}
	ctx := context.WithValue(context.Background(), standardRequestKey{}, state)
	ctx = context.WithValue(ctx, identityKey{}, &requestIdentity{Tenant: "north"})
	loader := newResolverLoader(app, ctx, "p1")
	plan := stagedPlan(t, snapshotConfig())
	input, err := buildAggregatePipelineInput(ctx, plan, map[string]any{"extra": 3}, nil, "p1")
	if err != nil {
		t.Fatal(err)
	}
	single, err := loader.loadStagedPipeline(plan, input)()
	if err != nil || fmt.Sprint(single.(map[string]any)["total"]) != "5" {
		t.Fatal(single, err)
	}
	strictConfig := snapshotConfig()
	strictConfig["stages"].([]any)[0].(map[string]any)["max_bytes"] = 1
	strict := stagedPlan(t, strictConfig)
	strictInput, _ := buildAggregatePipelineInput(ctx, strict, map[string]any{"extra": 3}, nil, "p1")
	if !reflect.DeepEqual(input, strictInput) {
		t.Fatal("fixture must differ only in validation budget")
	}
	if value, err := loader.loadStagedPipeline(strict, strictInput)(); value != nil || errorCode(err) != "response_limit_exceeded" {
		t.Fatal("lenient result reused", value, err)
	}
	rowsPlan := *plan
	rowsPlan.Result = "rows"
	rows, err := loader.loadStagedPipeline(&rowsPlan, input)()
	if err != nil || len(rows.([]any)) != 1 {
		t.Fatal(rows, err)
	}
	if platform.calls != 3 {
		t.Fatal("contracts incorrectly shared", platform.calls)
	}
	if state.metrics.BackendCalls != 3 || state.metrics.BackendReads != 6 {
		t.Fatal(state.metrics)
	}
	// Distinct aliases reserve the entire declared graph, not only final rows.
	limits := releaseLimits{MaxRows: 3, MaxResponseBytes: 4096, MaxCost: 100}
	budget := &standardRequest{limits: limits}
	if err := budget.reservePipeline(plan); err != nil {
		t.Fatal(err)
	}
	if err := budget.reservePipeline(plan); errorCode(err) != "row_limit_exceeded" {
		t.Fatal(err)
	}
}

func TestSnapshotPipelineConcurrentAuthorizationIsolation(t *testing.T) {
	unblock := make(chan struct{})
	defer close(unblock)
	p := &stagedPlatform{db: stagedDB(t), entered: make(chan struct{}), block: unblock}
	app := stagedReleaseFixture(t, p, snapshotConfig(), map[string]any{"coalesce_reads": true})
	identities := []*requestIdentity{
		{Subject: "same-user", Tenant: "north", Permissions: []string{"read"}, Expires: time.Now().Add(time.Hour)},
		{Subject: "other-user", Tenant: "south", Permissions: []string{"read"}, Expires: time.Now().Add(time.Hour)},
		{Subject: "same-user", Tenant: "north", Permissions: []string{"other"}, Expires: time.Now().Add(time.Hour)},
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 3)
	for _, identity := range identities {
		go func(identity *requestIdentity) {
			ctx := context.WithValue(cancelCtx, identityKey{}, identity)
			_, _ = app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `query Read {summary{total}}`})
			done <- struct{}{}
		}(identity)
	}
	eventuallyRuntime(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.calls == 3 })
	app.runtime.mu.Lock()
	flights := len(app.runtime.flights)
	app.runtime.mu.Unlock()
	if flights != 3 {
		t.Fatal("different verified users/scopes shared an execution", flights)
	}
	cancel()
	for range identities {
		<-done
	}
}

func TestSnapshotPipelineScalarBindingsAndIndependentSQLBudgets(t *testing.T) {
	t.Run("array index one", func(t *testing.T) {
		c := snapshotConfig()
		facts := c["stages"].([]any)[0].(map[string]any)
		facts["sql"] = "SELECT json_object('n',id) FROM {prospects} WHERE centre_id=? ORDER BY id"
		facts["max_rows"] = 2
		c["stages"].([]any)[1].(map[string]any)["params"].([]any)[0].(map[string]any)["from"] = "$stage.facts.rows.1.payload"
		value, err := snapshotRead(t, c)
		if err != nil || fmt.Sprint(value.(map[string]any)["total"]) != "5" {
			t.Fatal(value, err)
		}
	})
	t.Run("SQL Boolean", func(t *testing.T) {
		c := snapshotConfig()
		c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT 1 FROM {prospects} WHERE centre_id=? LIMIT 1"
		summary := c["stages"].([]any)[1].(map[string]any)
		summary["sql"] = "SELECT ? + ?"
		summary["params"].([]any)[0].(map[string]any)["type"] = "boolean"
		value, err := snapshotRead(t, c)
		if err != nil || fmt.Sprint(value.(map[string]any)["total"]) != "4" {
			t.Fatal(value, err)
		}
	})
	t.Run("optional NULL", func(t *testing.T) {
		c := snapshotConfig()
		c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT NULL FROM {prospects} WHERE centre_id=? LIMIT 1"
		summary := c["stages"].([]any)[1].(map[string]any)
		summary["sql"] = "SELECT COALESCE(?,0) + ?"
		binding := summary["params"].([]any)[0].(map[string]any)
		binding["type"], binding["required"] = "number", false
		value, err := snapshotRead(t, c)
		if err != nil || fmt.Sprint(value.(map[string]any)["total"]) != "3" {
			t.Fatal(value, err)
		}
	})
	t.Run("graph SQL greater than 64KiB", func(t *testing.T) {
		c := snapshotConfig()
		for _, raw := range c["stages"].([]any) {
			stage := raw.(map[string]any)
			stage["sql"] = stage["sql"].(string) + " /*" + strings.Repeat("x", 40000) + "*/"
		}
		value, err := snapshotRead(t, c)
		if err != nil || fmt.Sprint(value.(map[string]any)["total"]) != "5" {
			t.Fatal(value, err)
		}
	})
	t.Run("request JSON cannot inject reference", func(t *testing.T) {
		c := snapshotConfig()
		c["stages"].([]any)[1].(map[string]any)["params"].([]any)[1].(map[string]any)["type"] = "json"
		plan := stagedPlan(t, c)
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north"})
		input, err := buildAggregatePipelineInput(ctx, plan, map[string]any{"extra": map[string]any{"$ref": "facts.rows.0.secret"}}, nil, "p1")
		if err != nil {
			t.Fatal(err)
		}
		params := input["operations"].([]map[string]any)[1]["args"].(map[string]any)["params"].([]any)
		if _, ok := params[1].(string); !ok {
			t.Fatal("caller controls graph", params)
		}
	})
	t.Run("parent dependencies over 1000 total parameters", func(t *testing.T) {
		c := snapshotConfig()
		for _, item := range c["stages"].([]any) {
			stage := item.(map[string]any)
			params := []any{}
			for range 600 {
				params = append(params, map[string]any{"from": "$parent.owner.id", "type": "string"})
			}
			stage["params"] = params
		}
		if got := aggregatePipelineParentDependencies(c); !reflect.DeepEqual(got, []string{"owner"}) {
			t.Fatal(got)
		}
	})
}
