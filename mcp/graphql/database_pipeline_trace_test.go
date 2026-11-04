package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tracedFilter = `{"and":[` + authorisedFilter + `,` + selectedCentres + `]}`

func tracedContext(subject string, permissions ...string) (context.Context, *standardRequest) {
	state := &standardRequest{}
	return context.WithValue(pipelineIdentity(subject, permissions...), standardRequestKey{}, state), state
}

func tracedPlan(t *testing.T, centres ...any) map[string]any {
	t.Helper()
	plan := conditionalPlan(t, tracedFilter)
	plan["graphql_args"] = map[string]any{"centres": centres}
	return plan
}

// reportingDatabase answers like a Database that supports count diagnostics.
func reportingDatabase(allowed ...any) func([]any) (any, error) {
	answer := conditionalResponder(allowed...)
	return func(operations []any) (any, error) {
		out, err := answer(operations)
		if err != nil {
			return nil, err
		}
		results := out.(map[string]any)["results"].([]any)
		for i, raw := range operations {
			if raw.(map[string]any)["args"].(map[string]any)["diagnostics"] == true {
				results[i].(map[string]any)["diagnostics"] = map[string]any{
					"executed": true, "executionMs": 12.5, "planSource": "execution", "strategy": "index_count",
					"plan":   map[string]any{"access": "index", "index": "leads_centre", "ranges": 3, "fields": []any{"centre_id"}},
					"exists": map[string]any{"probes": 40, "keySetsBuilt": 1}, "documentFetches": 0, "leak": []any{"centre-a"},
				}
			}
		}
		return out, nil
	}
}

// refusingDatabase answers like Database 0.8.0, whose strict decoder rejects an
// option it doesn't know before any read runs.
func refusingDatabase(allowed ...any) func([]any) (any, error) {
	answer := conditionalResponder(allowed...)
	return func(operations []any) (any, error) {
		for _, raw := range operations {
			if _, asked := raw.(map[string]any)["args"].(map[string]any)["diagnostics"]; asked {
				return nil, errors.New(`database.db_batch: invalid_argument: invalid request: json: unknown field "diagnostics" (code=-32000)`)
			}
		}
		return answer(operations)
	}
}

func TestPipelineTraceReportsStagesAndCorrelatesTheFilter(t *testing.T) {
	a, p := pipelineApp(t, reportingDatabase("centre-a", "centre-b"))
	ctx, state := tracedContext("42")
	value, err := a.callDatabasePipeline(context.WithValue(ctx, pipelineFieldKey{}, "Query.leads"), tracedPlan(t, "centre-a"))
	if err != nil {
		t.Fatal(err)
	}
	// One access read, then one batch holding the page and one logical count.
	if len(p.batches) != 2 {
		t.Fatalf("batches: %v", p.batches)
	}
	read := p.batches[1]["operations"].([]any)
	if len(read) != 2 {
		t.Fatalf("the count must stay one operation beside its page: %v", read)
	}
	access := p.batches[0]["operations"].([]any)[0].(map[string]any)["args"].(map[string]any)
	find := read[0].(map[string]any)["args"].(map[string]any)
	count := read[1].(map[string]any)["args"].(map[string]any)
	if _, asked := access["diagnostics"]; asked {
		t.Error("an access read asked for statistics")
	}
	if _, asked := find["diagnostics"]; asked {
		t.Error("the page asked for statistics")
	}
	if count["diagnostics"] != true {
		t.Errorf("count args: %v", count)
	}
	if !reflect.DeepEqual(find["where"], count["where"]) {
		t.Errorf("count_of must count the page's filter: %v vs %v", find["where"], count["where"])
	}
	// Statistics stop here: a client can't see them.
	encoded, _ := json.Marshal(value)
	for _, leak := range []string{"diagnostics", "executionMs", "leads_centre"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("client result carries %s: %s", leak, encoded)
		}
	}

	trace := state.pipelineTraces[0].snapshot()
	if trace.Field != "Query.leads" || trace.Outcome != "ok" || trace.Stage != "" || trace.ErrorCode != "" {
		t.Errorf("trace: %+v", trace)
	}
	if trace.Access.Batches != 1 || trace.Access.Reads != 1 {
		t.Errorf("access: %+v", trace.Access)
	}
	batch := trace.Batch
	if batch == nil || batch.Statistics != pipelineStatisticsReported || batch.RetriedWithoutStatistics || len(batch.Operations) != 2 {
		t.Fatalf("batch: %+v", batch)
	}
	page, total := batch.Operations[0], batch.Operations[1]
	want := pipelineFilterHash(find["where"])
	if want == "" || page.FilterHash != want || total.FilterHash != want || trace.FilterHash != want {
		t.Errorf("hashes: want %s, page %s, count %s, trace %s", want, page.FilterHash, total.FilterHash, trace.FilterHash)
	}
	if page.Step != "rows" || page.Op != "find" || page.Rows == nil || *page.Rows != 0 || page.ExecutionMS != nil || page.Statistics != nil {
		t.Errorf("page: %+v", page)
	}
	if total.Step != "total" || total.Op != "count" || total.CountOf != "rows" || total.Count != "1" {
		t.Errorf("count: %+v", total)
	}
	if total.ExecutionMS == nil || *total.ExecutionMS != 12.5 || batch.DatabaseReportedMS != 12.5 {
		t.Errorf("reported time: %+v", total)
	}
	plan, _ := total.Statistics["plan"].(map[string]any)
	if plan["index"] != "leads_centre" || total.Statistics["exists"] == nil || total.Statistics["strategy"] != "index_count" {
		t.Errorf("statistics: %+v", total.Statistics)
	}
	if _, kept := total.Statistics["leak"]; kept {
		t.Errorf("a list reached the log: %+v", total.Statistics)
	}
	if _, kept := plan["fields"]; kept {
		t.Errorf("a nested list reached the log: %+v", plan)
	}
	// Stages add up: nothing is counted twice or lost.
	sum := trace.Access.MS + trace.ResolutionMS + batch.WallMS + trace.AssembleMS
	if diff := sum - trace.TotalMS; diff < -0.01 || diff > 0.01 {
		t.Errorf("stages sum to %f of %f", sum, trace.TotalMS)
	}
	// A trace must survive the JSON round trip the request log applies to it.
	metrics := runtimeMetrics{Pipelines: []pipelineTrace{trace}}
	raw, _ := json.Marshal(metrics)
	var decoded runtimeMetrics
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Pipelines[0].FilterHash != want || decoded.Pipelines[0].Batch.Operations[1].CountOf != "rows" || *decoded.Pipelines[0].Batch.Operations[1].ExecutionMS != 12.5 {
		t.Errorf("round trip: %s", raw)
	}
}

// Everything sent to Database and returned to the caller is the same with and
// without statistics, apart from the option itself.
func TestPipelineTraceLeavesPermissionFiltersUntouched(t *testing.T) {
	normalise := func(batches []map[string]any) string {
		for _, batch := range batches {
			delete(batch, "timeoutMs")
			for _, raw := range batch["operations"].([]any) {
				delete(raw.(map[string]any)["args"].(map[string]any), "diagnostics")
			}
		}
		out, _ := json.Marshal(batches)
		return string(out)
	}
	for name, c := range map[string]struct {
		allowed []any
		args    map[string]any
		code    string
	}{
		"no selection":     {[]any{"centre-a", "centre-b"}, map[string]any{}, ""},
		"selected centre":  {[]any{"centre-a", "centre-b"}, map[string]any{"centres": []any{"centre-a"}}, ""},
		"outside the list": {[]any{"centre-a"}, map[string]any{"centres": []any{"centre-z"}}, ""},
		"no access":        {[]any{}, map[string]any{}, ""},
		"status filter":    {[]any{"centre-a"}, map[string]any{"status": "open"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			run := func(respond func(...any) func([]any) (any, error), refused bool) (string, string, string) {
				a, p := pipelineApp(t, respond(c.allowed...))
				if refused {
					a.rememberStatisticsRefused("p1")
				}
				plan := conditionalPlan(t, `{"and":[`+authorisedFilter+`,`+selectedCentres+`,`+statusFilter+`]}`)
				plan["graphql_args"] = c.args
				ctx, _ := tracedContext("42")
				value, err := a.callDatabasePipeline(ctx, plan)
				encoded, _ := json.Marshal(value)
				return string(encoded), errorCode(err), normalise(p.batches)
			}
			withValue, withCode, withBatches := run(reportingDatabase, false)
			plainValue, plainCode, plainBatches := run(conditionalResponder, true)
			if withValue != plainValue || withCode != plainCode {
				t.Errorf("result changed: %s (%s) vs %s (%s)", withValue, withCode, plainValue, plainCode)
			}
			if withBatches != plainBatches {
				t.Errorf("requests changed:\n%s\n%s", withBatches, plainBatches)
			}
		})
	}
}

func TestPipelineTraceFallsBackWhenDatabaseRefusesStatistics(t *testing.T) {
	a, p := pipelineApp(t, refusingDatabase("centre-a"))
	reads := func() [][]any {
		out := [][]any{}
		for _, batch := range p.batches {
			ops := batch["operations"].([]any)
			if ops[0].(map[string]any)["args"].(map[string]any)["collection"] != "profiles" {
				out = append(out, ops)
			}
		}
		return out
	}
	ctx, state := tracedContext("42")
	if _, err := a.callDatabasePipeline(ctx, tracedPlan(t, "centre-a")); err != nil {
		t.Fatalf("a refused option must not fail the request: %v", err)
	}
	sent := reads()
	if len(sent) != 2 {
		t.Fatalf("expected the refused batch and its resend: %v", sent)
	}
	refused := sent[0][1].(map[string]any)["args"].(map[string]any)
	resent := sent[1][1].(map[string]any)["args"].(map[string]any)
	if refused["diagnostics"] != true || resent["diagnostics"] != nil {
		t.Errorf("options: %v / %v", refused, resent)
	}
	if !reflect.DeepEqual(refused["where"], resent["where"]) {
		t.Errorf("the resend changed the filter: %v / %v", refused["where"], resent["where"])
	}
	trace := state.pipelineTraces[0].snapshot()
	if trace.Outcome != "ok" || trace.Batch.Statistics != pipelineStatisticsUnavailable || !trace.Batch.RetriedWithoutStatistics {
		t.Errorf("first trace: %+v", trace.Batch)
	}
	if trace.Batch.Operations[1].ExecutionMS != nil {
		t.Errorf("no statistics, no execution time: %+v", trace.Batch.Operations[1])
	}

	// The next request doesn't pay for the refusal again.
	p.batches = nil
	ctx, state = tracedContext("42")
	if _, err := a.callDatabasePipeline(ctx, tracedPlan(t, "centre-a")); err != nil {
		t.Fatal(err)
	}
	if sent := reads(); len(sent) != 1 || sent[0][1].(map[string]any)["args"].(map[string]any)["diagnostics"] != nil {
		t.Fatalf("second request: %v", sent)
	}
	if trace := state.pipelineTraces[0].snapshot(); trace.Batch.Statistics != pipelineStatisticsUnavailable || trace.Batch.RetriedWithoutStatistics {
		t.Errorf("second trace: %+v", trace.Batch)
	}

	// Another project has its own Database.
	if !a.wantsDatabaseStatistics("p2") {
		t.Error("a refusal in p1 applied to p2")
	}
	// An upgraded Database is found again later.
	a.statsRefused["p1"] = time.Now().Add(-databaseStatisticsRecheck)
	if !a.wantsDatabaseStatistics("p1") {
		t.Error("statistics were never asked for again")
	}
}

func TestPipelineTraceOtherFailuresAreNotRetried(t *testing.T) {
	failing := errors.New("database.db_batch: resource_limit: operation deadline exceeded (code=-32000)")
	a, p := pipelineApp(t, func(operations []any) (any, error) {
		if operations[0].(map[string]any)["args"].(map[string]any)["collection"] == "profiles" {
			return conditionalResponder("centre-a")(operations)
		}
		return nil, failing
	})
	ctx, state := tracedContext("42")
	_, err := a.callDatabasePipeline(ctx, tracedPlan(t, "centre-a"))
	if errorCode(err) != "resource_limit" {
		t.Fatalf("error: %v (%s)", err, errorCode(err))
	}
	if len(p.batches) != 2 {
		t.Fatalf("a failed read must not run twice: %v", p.batches)
	}
	if !a.wantsDatabaseStatistics("p1") {
		t.Error("an unrelated failure marked statistics as refused")
	}
	trace := state.pipelineTraces[0].snapshot()
	if trace.Outcome != "error" || trace.Stage != pipelineStageBatch || trace.ErrorCode != "resource_limit" || trace.Batch == nil {
		t.Errorf("trace: %+v", trace)
	}
	if want := pipelineFilterHash(p.batches[1]["operations"].([]any)[0].(map[string]any)["args"].(map[string]any)["where"]); trace.FilterHash != want {
		t.Errorf("a failed read must still identify its filter: %s vs %s", trace.FilterHash, want)
	}
}

func TestPipelineTraceRecordsWhereACallStopped(t *testing.T) {
	t.Run("access read failed", func(t *testing.T) {
		a, _ := pipelineApp(t, func([]any) (any, error) { return nil, errors.New("database.db_batch: resource_limit: too much") })
		ctx, state := tracedContext("42")
		if _, err := a.callDatabasePipeline(ctx, tracedPlan(t)); err == nil {
			t.Fatal("expected a failure")
		}
		trace := state.pipelineTraces[0].snapshot()
		if trace.Outcome != "error" || trace.Stage != pipelineStageAccess || trace.Batch != nil || trace.Access.Batches != 1 {
			t.Errorf("trace: %+v", trace)
		}
	})
	t.Run("no caller", func(t *testing.T) {
		a, p := pipelineApp(t, conditionalResponder("centre-a"))
		state := &standardRequest{}
		ctx := context.WithValue(context.Background(), standardRequestKey{}, state)
		if _, err := a.callDatabasePipeline(ctx, tracedPlan(t)); err == nil {
			t.Fatal("expected a refusal")
		}
		if len(p.batches) != 0 {
			t.Errorf("nothing may be read for no caller: %v", p.batches)
		}
		if trace := state.pipelineTraces[0].snapshot(); trace.Outcome != "error" || trace.Stage != pipelineStageResolution {
			t.Errorf("trace: %+v", trace)
		}
	})
	t.Run("filter matches nothing", func(t *testing.T) {
		// No accessible centres: the page and total are answered without a read,
		// exactly as before, and the trace says so.
		a, p := pipelineApp(t, reportingDatabase())
		ctx, state := tracedContext("42")
		value, err := a.callDatabasePipeline(ctx, tracedPlan(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.batches) != 1 {
			t.Fatalf("only the access read may reach Database: %v", p.batches)
		}
		if got := fmt.Sprint(value.(map[string]any)["total"]); got != "0" {
			t.Errorf("total: %v", got)
		}
		trace := state.pipelineTraces[0].snapshot()
		if trace.Batch.Statistics != pipelineStatisticsNotRequested || !trace.Batch.Operations[0].AnsweredLocally || !trace.Batch.Operations[1].AnsweredLocally {
			t.Errorf("trace: %+v", trace.Batch)
		}
	})
}

func TestPipelineTraceSnapshotWhileRunning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	answer := reportingDatabase("centre-a")
	a, _ := pipelineApp(t, func(operations []any) (any, error) {
		if operations[0].(map[string]any)["args"].(map[string]any)["collection"] != "profiles" {
			close(entered)
			<-release
		}
		return answer(operations)
	})
	ctx, state := tracedContext("42")
	done := make(chan error, 1)
	go func() {
		_, err := a.callDatabasePipeline(ctx, tracedPlan(t, "centre-a"))
		done <- err
	}()
	<-entered
	time.Sleep(5 * time.Millisecond)
	state.timingMu.Lock()
	recorder := state.pipelineTraces[0]
	state.timingMu.Unlock()
	trace := recorder.snapshot()
	if trace.Outcome != "in_flight" || trace.Stage != pipelineStageBatch || trace.Batch.Statistics != pipelineStatisticsRequested || trace.FilterHash == "" {
		t.Errorf("in flight: %+v %+v", trace, trace.Batch)
	}
	if trace.Batch.WallMS < 1 || trace.TotalMS < trace.Batch.WallMS {
		t.Errorf("an unfinished batch must count up: %+v", trace.Batch)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if trace := recorder.snapshot(); trace.Outcome != "ok" || trace.Stage != "" || trace.Batch.Statistics != pipelineStatisticsReported {
		t.Errorf("finished: %+v %+v", trace, trace.Batch)
	}
}

// deadlinePlatform is a Database that doesn't answer in time, and whose call
// outlives the deadline a little as a real HTTP call unwinding does.
type deadlinePlatform struct {
	dbPipelinePlatform
}

func (p *deadlinePlatform) CallAppResultContext(ctx context.Context, _, _ string, _ map[string]any, _ any) error {
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	return ctx.Err()
}

// graphql-go returns the moment the deadline passes, before the resolver has
// finished. The request log must still have the trace: this is the request
// someone will look it up for.
func TestPipelineTraceSurvivesTheExecutionDeadline(t *testing.T) {
	app := secureTestApp(t, &deadlinePlatform{})
	db := app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query { calls: Page } type Page { rows: [Row!]! total: Int! } type Row { id: ID }`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	source, err := createSourceForAPI(db, "p1", "default", "crm", "database", map[string]any{"database": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	config := decodeConfig(t, `{"steps": [
	    {"id": "rows", "op": "find", "max_rows": 10, "args": {"collection": "calls", "where": {"field": "centre_id", "op": "eq", "value": "centre-a"}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total"}}`)
	if _, err := upsertResolverForAPI(db, "p1", "default", "Query", "calls", databasePipelineOperation, source.ID, config); err != nil {
		t.Fatal(err)
	}
	if _, err := publishAPIRelease(db, "p1", "default", "production", schema.Version, map[string]any{"max_execution_ms": 15}); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")

	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ calls { total } }"}`))
	request.Header.Set("X-GraphQL-Environment", "production")
	response := httptest.NewRecorder()
	app.handleGraphQL(response, request)
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "execution_timeout") {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, leak := range []string{"filter_hash", "diagnostics", "pipelines"} {
		if strings.Contains(response.Body.String(), leak) {
			t.Errorf("response carries %s: %s", leak, response.Body.String())
		}
	}
	if header := response.Header().Get("Server-Timing"); strings.Contains(header, "filter") || strings.Contains(header, "hash") {
		t.Errorf("Server-Timing carries the filter: %s", header)
	}
	app.stopRequestLogger()
	logs, err := publicLogsFiltered(app.ctx.AppDB(), "p1", defaultLogFilter())
	if err != nil || len(logs) != 1 {
		t.Fatal(logs, err)
	}
	pipelines, _ := logs[0]["runtime"].(map[string]any)["pipelines"].([]any)
	if len(pipelines) != 1 {
		t.Fatalf("the timed-out request lost its trace: %v", logs[0]["runtime"])
	}
	trace := pipelines[0].(map[string]any)
	if trace["outcome"] != "in_flight" || trace["stage"] != pipelineStageBatch || trace["field"] != "Query.calls" || trace["filter_hash"] == "" {
		t.Errorf("trace: %v", trace)
	}
	batch := trace["database_batch"].(map[string]any)
	if batch["statistics"] != pipelineStatisticsRequested || len(batch["operations"].([]any)) != 2 || batch["wall_ms"].(float64) < 5 {
		t.Errorf("batch: %v", batch)
	}
	timings := logs[0]["timings"].(map[string]any)
	if _, ok := timings["serialise"]; !ok {
		t.Errorf("no serialise stage: %v", timings)
	}
	if _, ok := timings["write"]; !ok {
		t.Errorf("no write stage: %v", timings)
	}
}

func TestPipelineWireSeparatesClientFromServerWait(t *testing.T) {
	const body = `{"results":[{"count":"1"}]}`
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			io.WriteString(w, body)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	call := func(ctx context.Context) (*pipelineWire, error) {
		wire := &pipelineWire{started: time.Now()}
		request, _ := http.NewRequestWithContext(wire.attach(ctx), http.MethodPost, server.URL, nil)
		response, err := server.Client().Do(request)
		if err == nil {
			defer response.Body.Close()
			raw, _ := io.ReadAll(response.Body)
			out := databaseBatchResponse{wire: wire}
			if err = json.Unmarshal(raw, &out); err == nil && len(out.Results) != 1 {
				err = errors.New("results lost")
			}
		}
		wire.returned()
		return wire, err
	}

	// Abandoned while the server is thinking: all of it shows as server wait.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	wire, err := call(ctx)
	if err == nil {
		t.Fatal("expected the deadline")
	}
	report := wire.report(time.Now())
	// A slow machine can spend the deadline connecting; either way the time is
	// shown in the stage it was stuck in, and nothing was read or decoded.
	if report.ConnectionWaitMS+report.RequestWriteMS+report.ServerWaitMS < 25 || report.DecodeMS != 0 || report.ResponseBytes != 0 {
		t.Errorf("abandoned: %+v", report)
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		close(release)
	}()
	wire, err = call(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report = wire.report(time.Now())
	if report.ServerWaitMS < 20 || report.ResponseBytes != int64(len(body)) {
		t.Errorf("answered: %+v", report)
	}
	if report.PrepareMS > report.ServerWaitMS || report.ConnectionWaitMS > report.ServerWaitMS || report.DecodeMS > report.ServerWaitMS {
		t.Errorf("client stages took the server's wait: %+v", report)
	}
}

func TestPipelineFilterHash(t *testing.T) {
	a := map[string]any{"field": "centre_id", "op": "in", "value": []any{"a", "b"}}
	b := map[string]any{"value": []any{"a", "b"}, "op": "in", "field": "centre_id"}
	if pipelineFilterHash(a) == "" || pipelineFilterHash(a) != pipelineFilterHash(b) {
		t.Error("key order must not change the hash")
	}
	if pipelineFilterHash(a) == pipelineFilterHash(map[string]any{"field": "centre_id", "op": "in", "value": []any{"a"}}) {
		t.Error("a narrower scope must change the hash")
	}
	if pipelineFilterHash(nil) != "" {
		t.Error("no filter has no hash")
	}
	if strings.Contains(pipelineFilterHash(a), "centre") {
		t.Error("the hash exposes the filter")
	}
}

func TestDatabaseStatisticsAreBounded(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := boundedDatabaseStatistics(map[string]any{"executionMs": 1.5, "indexOnly": true, "name": "short", "long": long, "values": []any{"a"},
		"plan": map[string]any{"index": "i", "long": long, "fields": []any{"f"}}})
	if out["executionMs"] != 1.5 || out["indexOnly"] != true || out["name"] != "short" || out["long"] != nil || out["values"] != nil {
		t.Errorf("scalars: %v", out)
	}
	plan := out["plan"].(map[string]any)
	if plan["index"] != "i" || plan["long"] != nil || plan["fields"] != nil {
		t.Errorf("nested: %v", plan)
	}
	huge := map[string]any{"executionMs": 1.5, "plan": map[string]any{}}
	for i := 0; i < 400; i++ {
		huge["plan"].(map[string]any)[strings.Repeat("k", 30)+string(rune('a'+i%26))+string(rune('a'+i/26))] = strings.Repeat("v", 100)
	}
	bounded := boundedDatabaseStatistics(huge)
	if encoded, _ := json.Marshal(bounded); len(encoded) > maxDatabaseStatisticsBytes || bounded["truncated"] != true || bounded["executionMs"] != 1.5 {
		t.Errorf("oversized statistics: %d bytes", len(encoded))
	}
}

func TestPipelineTracesPerRequestAreBounded(t *testing.T) {
	state := &standardRequest{}
	ctx := context.WithValue(context.Background(), standardRequestKey{}, state)
	for i := 0; i < maxPipelineTraces+3; i++ {
		startPipelineTrace(ctx).finish(nil)
	}
	if len(state.pipelineTraces) != maxPipelineTraces || state.metrics.PipelinesOmitted != 3 {
		t.Errorf("kept %d, omitted %d", len(state.pipelineTraces), state.metrics.PipelinesOmitted)
	}
	// A call outside a standard request has nowhere to report, and must still work.
	startPipelineTrace(context.Background()).finish(errors.New("x"))
	var none *pipelineTraceRecorder
	none.beginBatch(pipelineStageBatch, nil, nil)
	none.answeredLocally(0)
	none.noteStatistics(nil)
	none.statisticsRefused()
	none.endBatch(nil, nil)
	none.finish(nil)
	if ctx, wire := none.attachWire(context.Background()); ctx == nil || wire != nil {
		t.Error("a nil recorder must not trace a call")
	}
}
