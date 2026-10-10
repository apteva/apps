package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type runtimeBackend struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu                                       sync.Mutex
	generation                               int
	snapshots                                map[string]int
	handles                                  []string
	modes                                    []string
	opens, closes, reads, batches, bulkReads int
	block                                    <-chan struct{}
	entered                                  chan struct{}
	enterOnce                                sync.Once
	fail, closeFail, invalidSnapshot         bool
	missingResult                            bool
}

func (b *runtimeBackend) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, fmt.Errorf("raw calls unsupported")
}
func (b *runtimeBackend) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	b.mu.Lock()
	if b.generation == 0 {
		b.generation = 1
	}
	var value any
	switch tool {
	case "snapshot_open":
		b.opens++
		handle := fmt.Sprint("snapshot", b.opens)
		if b.snapshots == nil {
			b.snapshots = map[string]int{}
		}
		b.snapshots[handle] = b.generation
		expires, _ := time.Parse(time.RFC3339Nano, input["expires_at"].(string))
		if b.invalidSnapshot {
			expires = expires.Add(time.Hour)
		}
		value = map[string]any{"handle": handle, "expires_at": expires}
	case "snapshot_close":
		b.closes++
		delete(b.snapshots, fmt.Sprint(input["snapshot_handle"]))
		if b.closeFail {
			b.mu.Unlock()
			return fmt.Errorf("close failed")
		}
		value = map[string]any{"closed": true}
	case "read", "read_many":
		b.reads++
		generation := b.generation
		if handle, ok := input["snapshot_handle"].(string); ok {
			b.handles = append(b.handles, handle)
			generation = b.snapshots[handle]
		}
		b.generation++ // simulate writes/publications between separate resolver reads
		result := func(args map[string]any) upstreamResult {
			data := map[string]any{"id": fmt.Sprint(args["id"]), "value": generation}
			result := upstreamResult{Value: data, Metadata: []map[string]any{{"name": "generic_projection", "generation": generation, "ready": true, "coverage_from": "2026-10-01T00:00:00Z", "secret": "must not escape"}}}
			if b.fail {
				result.Error = &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}{Code: "backend_failure", Message: "backend private details"}
			}
			return result
		}
		if tool == "read_many" {
			b.batches++
			results := map[string]upstreamResult{}
			for _, read := range input["reads"].([]upstreamRead) {
				if !b.missingResult {
					results[read.ID] = result(read.Arguments)
				}
			}
			value = map[string]any{"results": results}
		} else {
			value = result(input["arguments"].(map[string]any))
		}
	case "rows_search":
		b.bulkReads++
		predicates := input["where"].([]any)
		ids := predicates[0].(map[string]any)["value"].([]any)
		rows := []any{}
		for _, id := range ids {
			if fmt.Sprint(id) != "99" {
				rows = append(rows, map[string]any{"id": id, "name": fmt.Sprint("row", id)})
			}
		}
		value = map[string]any{"rows": rows}
	case "tables_batch":
		b.modes = append(b.modes, fmt.Sprint(input["mode"]))
		b.batches++
		results := map[string]any{}
		for _, op := range input["operations"].([]map[string]any) {
			results[op["id"].(string)] = map[string]any{"status": "ok", "result": map[string]any{"rows": []any{map[string]any{"value": b.generation}}, "projections": []any{map[string]any{"generation": b.generation, "ready": true, "name": "generic", "secret": "hidden"}}}}
		}
		b.generation++
		value = map[string]any{"results": results}
	default:
		b.mu.Unlock()
		return fmt.Errorf("unexpected tool %s.%s", app, tool)
	}
	block := b.block
	entered := b.entered
	shouldBlock := tool == "read" || tool == "read_many"
	b.mu.Unlock()
	if shouldBlock && entered != nil {
		b.enterOnce.Do(func() { close(entered) })
	}
	if shouldBlock && block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (b *runtimeBackend) CallAppResult(app, tool string, input map[string]any, out any) error {
	return b.CallAppResultContext(context.Background(), app, tool, input, out)
}
func runtimeReleaseFixture(t *testing.T, backend *runtimeBackend, limits map[string]any) *App {
	t.Helper()
	app := secureTestApp(t, backend)
	db := app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query { item(id: ID = "1"): Record } type Record { id: ID value: Int next: Record }`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	source, err := createSourceForAPI(db, "p1", "default", "backend", "upstream", map[string]any{"app": "backend", "read_tool": "read", "batch_tool": "read_many", "snapshot_open_tool": "snapshot_open", "snapshot_close_tool": "snapshot_close", "read_only": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ parent, name string }{{"Query", "item"}, {"Record", "next"}} {
		if _, err := upsertResolverForAPI(db, "p1", "default", field.parent, field.name, "get", source.ID, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := publishAPIRelease(db, "p1", "default", "production", schema.Version, limits); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")
	return app
}
func TestRequestSnapshotReusedAcrossResolverLevelsAndMetadata(t *testing.T) {
	backend := &runtimeBackend{}
	app := runtimeReleaseFixture(t, backend, map[string]any{"read_consistency": "request", "max_snapshot_ms": 1000})
	out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{ item { value next { value } } }`})
	if err != nil || len(out.Errors) > 0 {
		t.Fatal(out, err)
	}
	item := out.Data["item"].(map[string]any)
	if item["value"] != 1 || item["next"].(map[string]any)["value"] != 1 {
		t.Fatal("mixed read generations", item)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.opens != 1 || backend.closes != 1 || len(backend.handles) != 2 || backend.handles[0] != backend.handles[1] || len(backend.snapshots) != 0 {
		t.Fatal("snapshot lifecycle", backend.opens, backend.closes, backend.handles)
	}
	raw, _ := json.Marshal(out.Extensions)
	if strings.Contains(string(raw), "must not escape") || strings.Contains(string(raw), "snapshot1") || !strings.Contains(string(raw), "generation") {
		t.Fatal("unsafe or missing extensions", string(raw))
	}
	if len(out.Runtime.ResolverTimings) != 2 || len(out.Runtime.Sources) == 0 {
		t.Fatal("runtime diagnostics missing", out.Runtime)
	}
}
func TestUpstreamBulkAndDuplicateResolverReads(t *testing.T) {
	backend := &runtimeBackend{}
	app := runtimeReleaseFixture(t, backend, nil)
	out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{ a:item(id:1){id value} b:item(id:2){id value} duplicate:item(id:1){id value} }`})
	if err != nil || len(out.Errors) > 0 {
		t.Fatal(out, err)
	}
	if backend.reads != 1 || backend.batches != 1 || out.Runtime.LoaderHits != 1 {
		t.Fatal("duplicate reads not eliminated", backend.reads, backend.batches, out.Runtime)
	}
	if out.Data["a"].(map[string]any)["id"] != "1" || out.Data["b"].(map[string]any)["id"] != "2" {
		t.Fatal("bulk result distribution", out.Data)
	}
}
func TestExecutionSharesCanonicalAuthorizedRequests(t *testing.T) {
	unblock := make(chan struct{})
	backend := &runtimeBackend{entered: make(chan struct{}), block: unblock}
	app := runtimeReleaseFixture(t, backend, map[string]any{"coalesce_reads": true})
	done := make(chan executeResult, 2)
	fail := make(chan error, 2)
	go func() {
		out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `query Read($id: ID = "1") { item(id:$id){ value } }`})
		done <- out
		fail <- err
	}()
	<-backend.entered
	go func() {
		out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: "# caller two\nquery Read ($id: ID = \"1\") {\n item(id: $id) { value }\n}", Variables: map[string]any{"id": "1"}})
		done <- out
		fail <- err
	}()
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
	if <-fail != nil || <-fail != nil {
		t.Fatal("shared execute failed")
	}
	if backend.reads != 1 || a.Runtime.ExecutionID != b.Runtime.ExecutionID || a.Runtime.Coalesced == b.Runtime.Coalesced {
		t.Fatal("independent executions", backend.reads, a.Runtime, b.Runtime)
	}
	a.Data["item"].(map[string]any)["value"] = 999
	if b.Data["item"].(map[string]any)["value"] == 999 {
		t.Fatal("caller response mutation leaked")
	}
}
func TestPartialUpstreamFailureNotReusedAndSnapshotsCleaned(t *testing.T) {
	backend := &runtimeBackend{fail: true}
	app := runtimeReleaseFixture(t, backend, map[string]any{"coalesce_reads": true, "read_consistency": "request"})
	for range 2 {
		out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{ item { value } }`})
		if err != nil || len(out.Errors) != 1 {
			t.Fatal(out, err)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "backend private details") {
			t.Fatal("private failure exposed")
		}
	}
	if backend.reads != 2 || backend.opens != 2 || backend.closes != 2 {
		t.Fatal("failure reused or snapshot leaked", backend.reads, backend.opens, backend.closes)
	}
}
func TestSnapshotValidationAndCleanupFailureAreErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend *runtimeBackend
		code    string
	}{{"unbounded", &runtimeBackend{invalidSnapshot: true}, "snapshot_invalid"}, {"cleanup", &runtimeBackend{closeFail: true}, "snapshot_cleanup_failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			app := runtimeReleaseFixture(t, tc.backend, map[string]any{"read_consistency": "request"})
			out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{ item { value } }`})
			if err != nil || len(out.Errors) != 1 || out.Errors[0]["extensions"].(map[string]any)["code"] != tc.code {
				t.Fatal(out, err)
			}
			if tc.backend.closes != 1 {
				t.Fatal("snapshot was not closed")
			}
		})
	}
}
func TestTablesBulkGetUsesOneBackendReadAndPreservesMissingRows(t *testing.T) {
	backend := &runtimeBackend{}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(backend))}
	state := &standardRequest{limits: defaultReleaseLimits()}
	ctx := context.WithValue(context.Background(), standardRequestKey{}, state)
	loader := newResolverLoader(app, ctx, "p1")
	a := loader.load("rows_get", map[string]any{"table": "records", "id": 1, "select": []any{"name"}}, "get")
	duplicate := loader.load("rows_get", map[string]any{"table": "records", "id": 1, "select": []any{"name"}}, "get")
	b := loader.load("rows_get", map[string]any{"table": "records", "id": 2, "select": []any{"name"}}, "get")
	missing := loader.load("rows_get", map[string]any{"table": "records", "id": 99, "select": []any{"name"}}, "get")
	first, err := a()
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["name"] != "row1" || first.(map[string]any)["id"] != nil {
		t.Fatal(first)
	}
	if _, err := duplicate(); err != nil {
		t.Fatal(err)
	}
	second, err := b()
	if err != nil || second.(map[string]any)["name"] != "row2" {
		t.Fatal(second, err)
	}
	none, err := missing()
	if err != nil || none != nil {
		t.Fatal(none, err)
	}
	if backend.bulkReads != 1 || state.metrics.BackendReads != 1 || state.metrics.LoaderHits != 1 {
		t.Fatal("N backend reads remained", backend.bulkReads, state.metrics)
	}
}
func TestTablesSnapshotBatchUsesNativeModeAndRejectsRequestGuarantee(t *testing.T) {
	backend := &runtimeBackend{}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(backend))}
	limits := defaultReleaseLimits()
	limits.ReadConsistency = "batch"
	state := &standardRequest{limits: limits}
	loader := newResolverLoader(app, context.WithValue(context.Background(), standardRequestKey{}, state), "p1")
	a := loader.load("tables_query", map[string]any{"sql": "SELECT 1"}, "find")
	b := loader.load("tables_query", map[string]any{"sql": "SELECT 2"}, "find")
	if _, err := a(); err != nil {
		t.Fatal(err)
	}
	if _, err := b(); err != nil {
		t.Fatal(err)
	}
	if len(backend.modes) != 1 || backend.modes[0] != "read_snapshot" {
		t.Fatal(backend.modes)
	}
	limits.ReadConsistency = "request"
	adapter := &tablesUpstream{app: app, project: "p1"}
	var reads requestReads
	if _, err := reads.get(context.Background(), sourceRecord{ID: 1, Name: "tables"}, adapter, limits); errorCode(err) != "snapshot_unsupported" {
		t.Fatal(err)
	}
}
func TestExecutionDeadlineCancelsUpstreamAndLogsRuntime(t *testing.T) {
	backend := &runtimeBackend{block: make(chan struct{})}
	app := runtimeReleaseFixture(t, backend, map[string]any{"max_execution_ms": 15})
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ item { value } }"}`))
	request.Header.Set("X-GraphQL-Environment", "production")
	response := httptest.NewRecorder()
	app.handleGraphQL(response, request)
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "execution_timeout") {
		t.Fatal(response.Code, response.Body.String())
	}
	app.stopRequestLogger()
	logs, err := publicLogsFiltered(app.ctx.AppDB(), "p1", defaultLogFilter())
	if err != nil || len(logs) != 1 {
		t.Fatal(logs, err)
	}
	metrics := logs[0]["runtime"].(map[string]any)
	if metrics["backend_calls"] != float64(1) {
		t.Fatal("runtime was not logged", metrics)
	}
}
func TestReadAdapterConfigurationValidation(t *testing.T) {
	for _, input := range []map[string]any{{"app": "backend", "read_tool": "read"}, {"app": "backend", "read_tool": "read", "read_only": true, "snapshot_open_tool": "open"}, {"app": "backend", "read_tool": "read", "read_only": true, "batch_tool": 42}} {
		if validateUpstreamSource(input) == nil {
			t.Fatal("invalid adapter accepted", input)
		}
	}
}

func (b *runtimeBackend) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, fmt.Errorf("server batch unavailable")
}

func TestTablesPointReadsFuseThroughGraphQLExecution(t *testing.T) {
	backend := &runtimeBackend{}
	app := secureTestApp(t, backend)
	db := app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query { item(id: ID!): Row } type Row { name: String }`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(err, issues)
	}
	source, err := createSourceForAPI(db, "p1", "default", "records", "tables", map[string]any{"table": "records"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upsertResolverForAPI(db, "p1", "default", "Query", "item", "get", source.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := publishAPIRelease(db, "p1", "default", "production", schema.Version, nil); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")
	out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{a:item(id:1){name} b:item(id:2){name} duplicate:item(id:1){name} missing:item(id:99){name}}`})
	if err != nil || len(out.Errors) > 0 || backend.bulkReads != 1 || out.Runtime.BackendReads != 1 || out.Runtime.LoaderHits != 1 {
		t.Fatal(out, err, backend.bulkReads)
	}
	if out.Data["a"].(map[string]any)["name"] != "row1" || out.Data["missing"] != nil {
		t.Fatal(out.Data)
	}
}

func TestDifferentAuthorizedUsersExecuteIndependentlyAndDeniedCallerDoesNotJoin(t *testing.T) {
	unblock := make(chan struct{})
	backend := &runtimeBackend{block: unblock, entered: make(chan struct{})}
	app := runtimeReleaseFixture(t, backend, map[string]any{"coalesce_reads": true})
	policy := testSecurityPolicy()
	policy["fields"] = map[string]any{"Query.item": []any{"read"}}
	if _, err := setSecurity(app.ctx.AppDB(), "p1", "default", policy); err != nil {
		t.Fatal(err)
	}
	active, err := getActiveAPIRelease(app.ctx.AppDB(), "p1", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishAPIRelease(app.ctx.AppDB(), "p1", "default", "production", active.SchemaVersion, map[string]any{"coalesce_reads": true}); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")
	expiry := time.Now().Add(time.Minute)
	identity := func(subject string, permissions []string) context.Context {
		return context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: subject, Issuer: "apteva:auth:default", Project: "p1", API: "default", Tenant: "default", Permissions: permissions, Expires: expiry})
	}
	query := graphqlRequest{Query: `{item{value}}`}
	done := make(chan executeResult, 2)
	errors := make(chan error, 2)
	for _, subject := range []string{"alice", "bob"} {
		go func(subject string) {
			out, err := app.execute(identity(subject, []string{"read"}), "p1", "default", "production", query)
			done <- out
			errors <- err
		}(subject)
	}
	eventuallyRuntime(t, func() bool { backend.mu.Lock(); defer backend.mu.Unlock(); return backend.reads == 2 })
	if _, err := app.execute(identity("alice", nil), "p1", "default", "production", query); errorCode(err) != "permission_denied" {
		t.Fatal("denied caller joined work", err)
	}
	close(unblock)
	a, b := <-done, <-done
	if <-errors != nil || <-errors != nil {
		t.Fatal("authorized executions failed")
	}
	if a.Runtime.ExecutionID == b.Runtime.ExecutionID || a.Data["item"].(map[string]any)["value"] == b.Data["item"].(map[string]any)["value"] {
		t.Fatal("users shared results", a, b)
	}
}
func TestUpstreamMissingBatchResultsAndBatchCleanupFailuresFinish(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend *runtimeBackend
		limits  map[string]any
	}{{"omitted", &runtimeBackend{missingResult: true}, nil}, {"cleanup", &runtimeBackend{closeFail: true}, map[string]any{"read_consistency": "batch"}}} {
		t.Run(tc.name, func(t *testing.T) {
			app := runtimeReleaseFixture(t, tc.backend, tc.limits)
			out, err := app.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: `{a:item(id:1){value} b:item(id:2){value}}`})
			if err != nil || len(out.Errors) != 2 {
				t.Fatal("incomplete failed batch", out, err)
			}
		})
	}
}
func TestFailedServerAndNativeBatchCompleteAllFutures(t *testing.T) {
	platform := &serverBatchPlatform{serverErr: fmt.Errorf("server failed"), nativeErr: fmt.Errorf("native failed")}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	loader := newResolverLoader(app, context.Background(), "p1")
	a := loader.load("rows_search", map[string]any{"table": "one"}, "find")
	b := loader.load("rows_search", map[string]any{"table": "two"}, "find")
	if _, err := a(); err == nil {
		t.Fatal("failed batch succeeded")
	}
	if _, err := b(); err == nil {
		t.Fatal("failed future succeeded")
	}
}
