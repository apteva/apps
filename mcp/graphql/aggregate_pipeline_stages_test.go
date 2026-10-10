package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	gql "github.com/graphql-go/graphql"
)

func stagedConfig() map[string]any {
	return map[string]any{"version": 2, "engine": "tables_batch", "sources": []any{"prospects"}, "final_stage": "summary", "result": "single", "max_rows": 1, "stages": []any{
		map[string]any{"id": "eligible", "columns": []any{"id", "centre"}, "sql": "SELECT id, centre_id FROM {prospects} WHERE centre_id=? ORDER BY id", "params": []any{map[string]any{"from": "$identity.tenant", "type": "string"}}, "max_rows": 10, "max_bytes": 1024},
		map[string]any{"id": "summary", "columns": []any{"total"}, "sql": "SELECT COUNT(*) + ? FROM {stage:eligible}", "params": []any{map[string]any{"from": "$args.extra", "type": "number"}}, "max_rows": 1, "max_bytes": 1024},
	}}
}

func cloneStagedConfig() map[string]any {
	raw, _ := json.Marshal(stagedConfig())
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	return c
}
func stagedPlan(t *testing.T, c map[string]any) *aggregatePipelinePlan {
	t.Helper()
	p, err := validateAggregatePipeline(aggregatePipelineOperation, c, pipelineSources(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func stagedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE prospects(id INTEGER,centre_id TEXT); INSERT INTO prospects VALUES(1,'north'),(2,'north'),(3,'south')")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func runStagedSQL(ctx context.Context, db *sql.DB, input map[string]any) (map[string]any, error) {
	sql := strings.ReplaceAll(input["sql"].(string), "{prospects}", `"prospects"`)
	var out string
	if err := db.QueryRowContext(ctx, sql, input["params"].([]any)...).Scan(&out); err != nil {
		return nil, err
	}
	return map[string]any{"rows": []any{map[string]any{"__graphql_pipeline": out}}, "truncated": false, "projections": []any{map[string]any{"name": "sample", "generation": 7, "secret": "hidden"}}}, nil
}

func TestStagedPipelineSQLParityIdentityAndTopologicalBindings(t *testing.T) {
	c := stagedConfig()
	stages := c["stages"].([]any)
	c["stages"] = []any{stages[1], stages[0]}
	p := stagedPlan(t, c)
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north"})
	input, err := buildAggregatePipelineInput(ctx, p, map[string]any{"extra": 3, "tenant": "south"}, nil, "p1")
	if err != nil || fmt.Sprint(input["params"]) != "[north 3]" {
		t.Fatal(input, err)
	}
	value, err := runStagedSQL(ctx, stagedDB(t), input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeStagedPipeline(p, value)
	if err != nil {
		t.Fatal(err)
	}
	final, err := transformAggregatePipelineResult(p, decoded)
	if err != nil || final.(map[string]any)["total"] != float64(5) || len(final.(map[string]any)) != 1 {
		t.Fatal(final, err)
	}
	if _, err := buildAggregatePipelineInput(context.Background(), p, map[string]any{"extra": 3, "tenant": "north"}, nil, "p1"); err == nil {
		t.Fatal("unverified tenant accepted")
	}
}

func TestStagedPipelineRejectsInvalidGraphsAndSQL(t *testing.T) {
	tests := map[string]func(map[string]any){
		"unknown": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT COUNT(*) FROM {stage:missing}"
		},
		"cycle": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT * FROM {stage:summary}"
		},
		"unused": func(c map[string]any) { c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT ? FROM {prospects}" },
		"table authorization": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT * FROM {sales} WHERE id=?"
		},
		"numbered binding": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT ?1 FROM {stage:eligible}"
		},
		"statements": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT ? FROM {stage:eligible}; DELETE FROM {prospects}"
		},
		"raw internal stage": func(c map[string]any) {
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT ? FROM gql_stage_0_raw"
		},
		"binding count":    func(c map[string]any) { delete(c["stages"].([]any)[1].(map[string]any), "params") },
		"column collision": func(c map[string]any) { c["stages"].([]any)[0].(map[string]any)["columns"] = []any{"x", "X"} },
		"client SQL": func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["sql"] = map[string]any{"from": "$args.sql"}
		},
		"missing final": func(c map[string]any) { c["final_stage"] = "missing" },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			c := cloneStagedConfig()
			edit(c)
			if _, err := validateAggregatePipeline(aggregatePipelineOperation, c, pipelineSources(), 1); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	_, deps, count, err := stagedSQL("SELECT '?' AS marker FROM {stage:x} -- {stage:missing} ?\n WHERE x=? /* ? */", map[string]string{"x": "gql_stage_0"})
	if err != nil || count != 1 || !reflect.DeepEqual(deps, []string{"x"}) {
		t.Fatal(deps, count, err)
	}
	quoted := stagedConfig()
	quoted["stages"].([]any)[0].(map[string]any)["sql"] = "SELECT id, '?;{missing}' FROM {prospects} WHERE centre_id=? -- {undeclared}\n"
	stagedPlan(t, quoted)
	c := stagedConfig()
	sources := pipelineSources()
	sources[0].Config["identity_where"] = map[string]any{}
	if _, err := validateAggregatePipeline(aggregatePipelineOperation, c, sources, 1); err == nil {
		t.Fatal("scoped source silently inherited")
	}
}

func TestStagedPipelineBudgetsGateIntermediateAndEmptyFinal(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(map[string]any)
		code string
	}{
		{"rows", func(c map[string]any) { c["stages"].([]any)[0].(map[string]any)["max_rows"] = 1 }, "row_limit_exceeded"},
		{"bytes", func(c map[string]any) { c["stages"].([]any)[0].(map[string]any)["max_bytes"] = 1 }, "response_limit_exceeded"},
		{"empty final", func(c map[string]any) {
			c["stages"].([]any)[0].(map[string]any)["max_rows"] = 1
			c["stages"].([]any)[1].(map[string]any)["sql"] = "SELECT id+? FROM {stage:eligible} WHERE 0"
		}, "row_limit_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := stagedConfig()
			test.edit(c)
			p := stagedPlan(t, c)
			ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north"})
			input, _ := buildAggregatePipelineInput(ctx, p, map[string]any{"extra": 0}, nil, "p1")
			value, err := runStagedSQL(ctx, stagedDB(t), input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeStagedPipeline(p, value); errorCode(err) != test.code {
				t.Fatal(err)
			}
		})
	}
	p := stagedPlan(t, stagedConfig())
	if p.validateStageLimits(releaseLimits{MaxRows: 10, MaxResponseBytes: 2048}) == nil || p.validateStageLimits(releaseLimits{MaxRows: 11, MaxResponseBytes: 2047}) == nil {
		t.Fatal("cumulative release budgets ignored")
	}
	if err := p.validateStageLimits(releaseLimits{MaxRows: 11, MaxResponseBytes: 2048}); err != nil {
		t.Fatal(err)
	}
	c := stagedConfig()
	c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$parent.owner.id", "type": "string"}}
	if got := aggregatePipelineParentDependencies(c); !reflect.DeepEqual(got, []string{"owner"}) {
		t.Fatal(got)
	}
}

type stagedPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	db      *sql.DB
	mu      sync.Mutex
	calls   int
	block   <-chan struct{}
	entered chan struct{}
	once    sync.Once
	fail    bool
}

func (p *stagedPlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, fmt.Errorf("raw calls unsupported")
}

func (p *stagedPlatform) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if app != "tables" || tool != "tables_batch" || input["mode"] != "read_snapshot" {
		return fmt.Errorf("unexpected call %s %s %v", app, tool, input["mode"])
	}
	ops := input["operations"].([]map[string]any)
	if len(ops) != 1 || ops[0]["operation"] != "tables_query" {
		return fmt.Errorf("stages crossed app boundary")
	}
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.fail {
		return fmt.Errorf("backend failure")
	}
	value, err := runStagedSQL(ctx, p.db, ops[0]["args"].(map[string]any))
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"results": map[string]any{"final": map[string]any{"status": "ok", "result": value}}})
	return json.Unmarshal(raw, out)
}

func TestStagedPipelineStandardUsesSnapshotAndOnlyFinalRows(t *testing.T) {
	p := &stagedPlatform{db: stagedDB(t)}
	b := &executionBindings{sources: map[int64]sourceRecord{1: pipelineSources()[0]}, resolvers: map[string]resolverRecord{"Query.summary": {SourceID: 1, Operation: aggregatePipelineOperation, Config: stagedConfig()}}}
	_, schema, ctx := standardApp(t, `type Query { summary(extra:Int!): Metric! } type Metric {total:Int!}`, p, b)
	ctx = context.WithValue(ctx, identityKey{}, &requestIdentity{Tenant: "north"})
	ctx.Value(standardRequestKey{}).(*standardRequest).limits = defaultReleaseLimits()
	ctx.Value(standardRequestKey{}).(*standardRequest).loader.ctx = ctx
	r := runStandard(schema, gql.Params{Context: ctx, RequestString: `{summary(extra:3){total}}`})
	if len(r.Errors) > 0 || p.calls != 1 || r.Data.(map[string]any)["summary"].(map[string]any)["total"] != 5 {
		t.Fatal(r, p.calls)
	}
	state := ctx.Value(standardRequestKey{}).(*standardRequest)
	if state.metrics.BackendCalls != 1 || state.metrics.BackendReads != 2 || strings.Contains(fmt.Sprint(state.metadata), "hidden") || !strings.Contains(fmt.Sprint(state.metadata), "batch") {
		t.Fatal(state.metrics)
	}
}

func stagedReleaseFixture(t *testing.T, p *stagedPlatform, config map[string]any, limits map[string]any) *App {
	t.Helper()
	app := secureTestApp(t, p)
	db := app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query {summary(extra:Int=3): Metric} type Metric{total:Int!}`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	source, err := createSourceForAPI(db, "p1", "default", "prospects", "tables", map[string]any{"table": "prospects"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upsertResolverForAPI(db, "p1", "default", "Query", "summary", aggregatePipelineOperation, source.ID, config); err != nil {
		t.Fatal(err)
	}
	if _, err := publishAPIRelease(db, "p1", "default", "production", schema.Version, limits); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")
	return app
}

func TestStagedPipelineCoalescingAndAuthorizationIsolation(t *testing.T) {
	unblock := make(chan struct{})
	p := &stagedPlatform{db: stagedDB(t), entered: make(chan struct{}), block: unblock}
	app := stagedReleaseFixture(t, p, stagedConfig(), map[string]any{"coalesce_reads": true})
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "user1", Tenant: "north", Expires: time.Now().Add(time.Hour)})
	done := make(chan executeResult, 2)
	errs := make(chan error, 2)
	run := func(query string) {
		out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: query})
		done <- out
		errs <- err
	}
	go run(`query Read {summary{total}}`)
	<-p.entered
	go run("# second caller\nquery Read { summary { total } }")
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
		t.Fatal("projection metadata lost or leaked", a.Extensions)
	}
	other := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "user2", Tenant: "south", Expires: time.Now().Add(time.Hour)})
	out, err := app.execute(other, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
	if err != nil || len(out.Errors) > 0 || fmt.Sprint(out.Data["summary"].(map[string]any)["total"]) != "4" || p.calls != 2 {
		t.Fatal(out, err, p.calls)
	}
}

func TestStagedPipelineFailedExecutionsNotCachedAndCancellation(t *testing.T) {
	t.Run("stage failure", func(t *testing.T) {
		p := &stagedPlatform{db: stagedDB(t)}
		c := stagedConfig()
		c["stages"].([]any)[0].(map[string]any)["max_rows"] = 1
		app := stagedReleaseFixture(t, p, c, map[string]any{"coalesce_reads": true})
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
		for range 2 {
			out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
			if err != nil || len(out.Errors) != 1 || fmt.Sprint(out.Errors[0]["extensions"]) != "map[code:row_limit_exceeded]" {
				t.Fatal(out, err)
			}
		}
		if p.calls != 2 {
			t.Fatal("failed result reused", p.calls)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		p := &stagedPlatform{db: stagedDB(t), block: make(chan struct{})}
		app := stagedReleaseFixture(t, p, stagedConfig(), map[string]any{"max_execution_ms": 30})
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north"})
		start := time.Now()
		out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
		if time.Since(start) > time.Second || err == nil && len(out.Errors) == 0 {
			t.Fatal("cancellation ignored", out, err)
		}
	})
}

func TestStagedPipelineRequestBudgetsAndResultShapeIsolation(t *testing.T) {
	p := &stagedPlatform{db: stagedDB(t)}
	c := stagedConfig()
	c["stages"].([]any)[0].(map[string]any)["params"] = []any{map[string]any{"from": "$args.centre", "type": "string"}}
	plan := stagedPlan(t, c)
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))}
	state := &standardRequest{limits: defaultReleaseLimits()}
	ctx := context.WithValue(context.Background(), standardRequestKey{}, state)
	loader := newResolverLoader(app, ctx, "p1")
	input, _ := buildAggregatePipelineInput(ctx, plan, map[string]any{"centre": "north", "extra": 3}, nil, "p1")
	readSingle := loader.loadStagedPipeline(plan, input)
	rowsPlan := *plan
	rowsPlan.Result = "rows"
	readRows := loader.loadStagedPipeline(&rowsPlan, input)
	single, err := readSingle()
	if err != nil || single.(map[string]any)["total"] != float64(5) {
		t.Fatal(single, err)
	}
	rows, err := readRows()
	if err != nil || len(rows.([]any)) != 1 {
		t.Fatal(rows, err)
	}
	if p.calls != 2 {
		t.Fatal("different completion contracts incorrectly shared", p.calls)
	}
	for _, limit := range []struct {
		limits releaseLimits
		code   string
	}{
		{releaseLimits{MaxRows: 11, MaxResponseBytes: 4096, MaxCost: 100}, "row_limit_exceeded"},
		{releaseLimits{MaxRows: 100, MaxResponseBytes: 2048, MaxCost: 100}, "response_limit_exceeded"},
		{releaseLimits{MaxRows: 100, MaxResponseBytes: 4096, MaxCost: 11}, "query_cost_exceeded"},
	} {
		s := &standardRequest{limits: limit.limits}
		if err := s.reservePipeline(plan); err != nil {
			t.Fatal(err)
		}
		if err := s.reservePipeline(plan); errorCode(err) != limit.code {
			t.Fatal(err)
		}
	}
}
