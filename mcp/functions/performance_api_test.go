package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type performanceFixture struct {
	ctx   *sdk.AppCtx
	f     performanceFilter
	busy  *Function
	slow  *Function
	queue *Function
	run   *Function
}

func newPerformanceFixture(t *testing.T) performanceFixture {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	t.Setenv("APTEVA_PROJECT_ID", "")
	if err := migrateExecutionIdentity(context.Background(), ctx.AppDB(), nil); err != nil {
		t.Fatal(err)
	}
	f := performanceFilter{Since: time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC), Sort: "total_execution_ms", Limit: 50}
	create := func(pid, name string) *Function {
		fn, err := dbCreateFunction(ctx.AppDB(), pid, &Function{Name: name, Runtime: "node", SourceKind: "inline", Source: "private-source", SourceHash: "hash", Env: map[string]string{"SECRET": "private-env"}})
		if err != nil {
			t.Fatal(err)
		}
		return fn
	}
	x := performanceFixture{ctx: ctx, f: f, busy: create(testProj, "busy"), slow: create(testProj, "slow"), queue: create(testProj, "queue"), run: create(testProj, "running")}
	other := create("other-project", "foreign")
	for n := int64(1); n <= 20; n++ {
		status := "ok"
		if n == 19 {
			status = "canceled"
		} else if n == 20 {
			status = "error"
		}
		started := f.Since.Add(time.Duration(n-1) * time.Second).Format(time.RFC3339Nano)
		if n == 2 {
			started = f.Since.Add(time.Second).Format("2006-01-02 15:04:05") // legacy timestamp
		}
		x.add(t, testProj, x.busy.ID, started, status, n*10+100, n*10, 50)
	}
	x.add(t, testProj, x.slow.ID, f.Since.Add(time.Minute).Format(time.RFC3339Nano), "ok", 2000, 1000, 500)
	x.add(t, testProj, x.slow.ID, f.Since.Add(2*time.Minute).Format(time.RFC3339Nano), "ok", 2000, 900, 500)
	x.add(t, testProj, x.queue.ID, f.Since.Add(time.Minute).Format(time.RFC3339Nano), "timeout", 5000, 0, 5000)
	x.add(t, testProj, x.run.ID, f.Since.Add(time.Minute).Format(time.RFC3339Nano), "running", 99999, 99999, 99999)
	x.add(t, testProj, x.busy.ID, f.Until.Format(time.RFC3339Nano), "ok", 99999, 99999, 99999) // exclusive upper boundary
	x.add(t, testProj, x.busy.ID, f.Since.Add(-time.Millisecond).Format(time.RFC3339Nano), "ok", 99999, 99999, 99999)
	x.add(t, "other-project", other.ID, f.Since.Add(time.Minute).Format(time.RFC3339Nano), "ok", 99999, 99999, 99999)
	return x
}

func (x performanceFixture) add(t *testing.T, pid string, fid int64, started, status string, duration, execution, queue int64) int64 {
	t.Helper()
	finished := x.f.Until.Format(time.RFC3339Nano)
	if status == "running" {
		finished = ""
	}
	id, err := dbInsertInvocation(x.ctx.AppDB(), pid, &Invocation{FunctionID: fid, StartedAt: started, FinishedAt: finished, Status: status, DurationMS: duration, TriggerKind: "http", EventJSON: "private-payload", ResponseBody: "private-response", Stderr: "private-console", Identity: StoredResources(`{"subject":"private-subject"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.ctx.AppDB().Exec("UPDATE function_invocations SET execution_ms=?,queue_ms=?,build_ms=12,cold_start_ms=15 WHERE id=?", execution, queue, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (x performanceFixture) args(sort string) map[string]any {
	return map[string]any{"_project_id": testProj, "since": x.f.Since.Format(time.RFC3339Nano), "until": x.f.Until.Format(time.RFC3339Nano), "sort": sort}
}

func TestFunctionPerformanceExactStatsAndScope(t *testing.T) {
	x := newPerformanceFixture(t)
	report, err := dbFunctionPerformance(context.Background(), x.ctx.AppDB(), testProj, x.f)
	if err != nil {
		t.Fatal(err)
	}
	if report.FunctionCount != 4 || report.Totals != (performanceTotals{Calls: 24, Completed: 23, Running: 1, Errors: 2, Canceled: 1, TotalExecutionMS: 4000}) {
		t.Fatalf("counts: %+v", report)
	}
	b := report.Functions[0]
	if b.FunctionName != "busy" || b.Calls != 20 || b.Completed != 20 || b.Errors != 1 || b.Canceled != 1 || b.ErrorRate != .05 || b.CallsPerMinute != float64(20)/60 {
		t.Fatalf("busy stats: %+v", b)
	}
	if *b.AvgExecutionMS != 105 || *b.P95ExecutionMS != 190 || *b.MaxExecutionMS != 200 || *b.AvgDurationMS != 205 || *b.P95DurationMS != 290 || *b.AvgQueueMS != 50 {
		t.Fatalf("incorrect latency statistics: %+v", b)
	}
	r := report.Functions[3]
	if r.FunctionName != "running" || r.P95ExecutionMS != nil || r.AvgDurationMS != nil || r.TotalExecutionMS != 0 {
		t.Fatalf("running calls polluted finalized measurements: %+v", r)
	}
	for _, test := range []struct{ sort, first string }{{"calls", "busy"}, {"p95_execution_ms", "slow"}, {"p95_duration_ms", "queue"}, {"avg_queue_ms", "queue"}, {"error_rate", "queue"}} {
		x.f.Sort, x.f.Limit = test.sort, 1
		r, err := dbFunctionPerformance(context.Background(), x.ctx.AppDB(), testProj, x.f)
		if err != nil || len(r.Functions) != 1 || r.Functions[0].FunctionName != test.first || !r.HasMore || r.Totals.Calls != 24 {
			t.Fatalf("sort %s: %+v / %v", test.sort, r, err)
		}
	}
	bytes, _ := json.Marshal(report)
	if strings.Contains(string(bytes), "private-") || strings.Contains(string(bytes), "foreign") {
		t.Fatalf("ranking leaked private data: %s", bytes)
	}
}

func TestSlowInvocationsFiltersAndStablePagination(t *testing.T) {
	x := newPerformanceFixture(t)
	args := x.args("duration_ms")
	args["limit"], args["min_ms"] = 1, 1000
	seen := map[int64]bool{}
	for pageNo := 0; pageNo < 3; pageNo++ {
		f, err := slowFilter(x.ctx, testProj, args)
		if err != nil {
			t.Fatal(err)
		}
		page, err := dbSlowInvocations(context.Background(), x.ctx.AppDB(), testProj, f)
		if err != nil || len(page.Invocations) != 1 {
			t.Fatalf("page %d: %+v / %v", pageNo, page, err)
		}
		i := page.Invocations[0]
		if seen[i.ID] {
			t.Fatalf("duplicate across equal-timing pages: %+v", i)
		}
		seen[i.ID] = true
		if pageNo == 0 && i.FunctionName != "queue" || pageNo > 0 && i.FunctionName != "slow" {
			t.Fatalf("incorrect order: %+v", i)
		}
		if pageNo < 2 && page.NextCursor == "" || pageNo == 2 && page.NextCursor != "" {
			t.Fatalf("incorrect cursor on page %d", pageNo)
		}
		args["cursor"] = page.NextCursor
		delete(args, "since")
		delete(args, "until") // cursor must preserve the original window
		bytes, _ := json.Marshal(page)
		if strings.Contains(string(bytes), "private-") {
			t.Fatalf("slow summaries leaked previews: %s", bytes)
		}
	}
	args = x.args("queue_ms")
	args["name"], args["status"], args["min_ms"] = "slow", "ok", 500
	f, err := slowFilter(x.ctx, testProj, args)
	if err != nil {
		t.Fatal(err)
	}
	page, err := dbSlowInvocations(context.Background(), x.ctx.AppDB(), testProj, f)
	if err != nil || len(page.Invocations) != 2 || page.Invocations[0].QueueMS != 500 {
		t.Fatalf("function/status/threshold filter: %+v / %v", page, err)
	}
	args["limit"] = 1
	f, _ = slowFilter(x.ctx, testProj, args)
	page, _ = dbSlowInvocations(context.Background(), x.ctx.AppDB(), testProj, f)
	args["cursor"] = page.NextCursor
	if _, err := slowFilter(x.ctx, "other-project", args); err == nil {
		t.Fatal("accepted another project's cursor")
	}
	args["sort"] = "duration_ms"
	if _, err := slowFilter(x.ctx, testProj, args); err == nil {
		t.Fatal("accepted cursor with changed sort")
	}
}

func TestPerformanceHTTPMCPParityAndLogDrilldown(t *testing.T) {
	x := newPerformanceFixture(t)
	old := globalCtx
	globalCtx = x.ctx
	t.Cleanup(func() { globalCtx = old })
	app := &App{}
	for _, slow := range []bool{false, true} {
		path, key := "/performance", "calls"
		if slow {
			path, key = "/invocations/slow", "execution_ms"
		}
		args := x.args(key)
		q := url.Values{"project_id": {testProj}}
		for k, v := range args {
			if k != "_project_id" {
				q.Set(k, fmt.Sprint(v))
			}
		}
		w := httptest.NewRecorder()
		app.handleHTTPPerformanceReport(w, httptest.NewRequest("GET", path+"?"+q.Encode(), nil), slow)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP: %d %s", w.Code, w.Body.String())
		}
		tool := performanceTool(app, slow)
		mcp, err := tool.HandlerCtx(context.Background(), x.ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		bytes, _ := json.Marshal(mcp)
		var httpValue, mcpValue any
		json.Unmarshal(w.Body.Bytes(), &httpValue)
		json.Unmarshal(bytes, &mcpValue)
		if !reflect.DeepEqual(httpValue, mcpValue) {
			t.Fatalf("HTTP/MCP reports differ: %s / %s", w.Body.String(), bytes)
		}
	}
	args := x.args("duration_ms")
	args["name"] = "slow"
	pageValue, err := app.toolSlowInvocations(context.Background(), x.ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	call := pageValue.(*slowInvocationsPage).Invocations[0]
	logs, err := app.toolLogs(x.ctx, map[string]any{"_project_id": testProj, "invocation_id": call.ID})
	if err != nil {
		t.Fatal(err)
	}
	out := logs.(map[string]any)
	if out["duration_ms"] != int64(2000) || out["execution_ms"] != call.ExecutionMS || out["queue_ms"] != int64(500) || out["build_ms"] != int64(12) || out["cold_start_ms"] != int64(15) {
		t.Fatalf("logs missing timing breakdown: %+v", out)
	}
	if _, err := app.toolLogs(x.ctx, map[string]any{"_project_id": "other-project", "invocation_id": call.ID}); err == nil {
		t.Fatal("logs leaked across projects")
	}
	for _, route := range []string{"/performance", "/invocations/slow"} {
		w := httptest.NewRecorder()
		app.handleHTTPPerformanceReport(w, httptest.NewRequest("POST", route, nil), route != "/performance")
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal("performance endpoint accepted a write method")
		}
	}
}

func TestPerformanceValidationEmptyAndCancellation(t *testing.T) {
	x := newPerformanceFixture(t)
	for _, number := range []any{float64(1234567), json.Number("1e6"), "1234567"} {
		if _, err := performanceNumber(map[string]any{"min_ms": number}, "min_ms", 0, 0, 1<<53-1); err != nil {
			t.Fatalf("rejected integral JSON number %v: %v", number, err)
		}
	}
	for _, args := range []map[string]any{{"window": "forever"}, {"since": "yesterday"}, {"since": "2026-01-01T00:00:00Z"}, {"since": x.f.Until.Format(time.RFC3339), "until": x.f.Since.Format(time.RFC3339)}, {"sort": "duration_ms;DROP TABLE functions"}, {"limit": 201}, {"limit": 1.5}, {"min_ms": -1}, {"status": "running"}} {
		if _, err := parsePerformanceFilter(args, x.f.Until, true); err == nil {
			t.Fatalf("accepted invalid arguments: %+v", args)
		}
	}
	if _, err := slowFilter(x.ctx, testProj, map[string]any{"cursor": "bad-cursor"}); err == nil {
		t.Fatal("accepted invalid cursor")
	}
	if _, err := performanceFunctionID(x.ctx, testProj, map[string]any{"name": "foreign"}); err == nil {
		t.Fatal("function lookup leaked across projects")
	}
	if _, err := performanceFunctionID(x.ctx, testProj, map[string]any{"id": x.busy.ID, "name": "slow"}); err == nil {
		t.Fatal("accepted contradictory function selector")
	}
	for _, scoped := range []bool{false, true} {
		if scoped {
			t.Setenv("APTEVA_PROJECT_ID", testProj)
		}
		args := x.args("calls")
		args["_project_id"] = "other-project"
		value, err := (&App{}).toolPerformance(context.Background(), x.ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		calls := value.(*performanceReport).Totals.Calls
		if scoped && calls != 24 || !scoped && calls != 1 {
			t.Fatalf("incorrect project scope: %v / %d", scoped, calls)
		}
	}
	x.f.Since, x.f.Until = x.f.Until.Add(time.Hour), x.f.Until.Add(2*time.Hour)
	report, err := dbFunctionPerformance(context.Background(), x.ctx.AppDB(), testProj, x.f)
	if err != nil || len(report.Functions) != 0 || report.Functions == nil || report.Totals.Calls != 0 {
		t.Fatalf("empty report: %+v / %v", report, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dbFunctionPerformance(canceled, x.ctx.AppDB(), testProj, x.f); err == nil {
		t.Fatal("report ignored cancellation")
	}
}
