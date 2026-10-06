package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func eventuallyRuntime(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("runtime condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestSharedExecutionWaiterCancellationAndNoResultCache(t *testing.T) {
	var runtime executionRuntime
	limits := defaultReleaseLimits()
	first, cancelFirst := context.WithCancel(context.Background())
	entered, complete := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	run := func(ctx context.Context) (executeResult, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-complete:
			return executeResult{Data: map[string]any{"value": 1}, Errors: []map[string]any{{"message": "partial failure", "path": []any{"value"}}}}, nil
		case <-ctx.Done():
			return executeResult{}, ctx.Err()
		}
	}
	firstDone := make(chan error, 1)
	go func() { _, err := runtime.share(first, "same", limits, run); firstDone <- err }()
	<-entered
	secondDone := make(chan executeResult, 1)
	go func() {
		out, err := runtime.share(context.Background(), "same", limits, run)
		if err != nil {
			t.Error(err)
		}
		secondDone <- out
	}()
	eventuallyRuntime(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return runtime.flights["same"] != nil && runtime.flights["same"].waiters == 2
	})
	cancelFirst()
	if err := <-firstDone; errorCode(err) != "execution_cancelled" {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("one cancelled waiter interrupted the flight")
	}
	close(complete)
	second := <-secondDone
	if !second.Runtime.Coalesced || second.Runtime.Waiters != 2 || second.Data["value"] != json.Number("1") || len(second.Errors) != 1 {
		t.Fatalf("shared result: %+v", second)
	}
	// The partial result was shared with its concurrent waiter, then forgotten.
	_, err := runtime.share(context.Background(), "same", limits, func(context.Context) (executeResult, error) {
		calls.Add(1)
		return executeResult{}, errors.New("failed")
	})
	if err == nil || calls.Load() != 2 {
		t.Fatal("partial result was cached")
	}
	_, _ = runtime.share(context.Background(), "same", limits, func(context.Context) (executeResult, error) { calls.Add(1); return executeResult{}, nil })
	if calls.Load() != 3 {
		t.Fatal("failed result was cached")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.flights) != 0 {
		t.Fatal("completed flights retained")
	}
}
func TestLastWaiterCancelsUpstreamAndWaiterLimit(t *testing.T) {
	var runtime executionRuntime
	l := defaultReleaseLimits()
	l.MaxCoalescedWaiters = 1
	ctx, cancel := context.WithCancel(context.Background())
	entered, stopped := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runtime.share(ctx, "one", l, func(ctx context.Context) (executeResult, error) {
			close(entered)
			<-ctx.Done()
			close(stopped)
			return executeResult{}, ctx.Err()
		})
	}()
	<-entered
	if _, err := runtime.share(context.Background(), "one", l, func(context.Context) (executeResult, error) {
		t.Error("excess waiter executed")
		return executeResult{}, nil
	}); errorCode(err) != "coalescing_limit" {
		t.Fatal(err)
	}
	cancel()
	<-done
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("last waiter did not cancel upstream")
	}
}
func TestAuthorizedSharingKeySeparatesEveryScope(t *testing.T) {
	release := &apiRelease{ID: 1, Checksum: "immutable", Limits: defaultReleaseLimits()}
	identity := &requestIdentity{Subject: "alice", Issuer: "auth", Project: "p", API: "api", Tenant: "tenant", Claims: map[string]any{"role": "read", "authorization_version": 1}, Permissions: []string{"read"}, Expires: time.Now().Add(time.Minute)}
	ctx := context.WithValue(context.Background(), identityKey{}, identity)
	key, _ := coalescingKey(ctx, "p", "api", "production", release, "{ value }", "", map[string]any{"id": 1})
	cases := []struct {
		name   string
		mutate func(*requestIdentity)
	}{
		{"user", func(i *requestIdentity) { i.Subject = "bob" }}, {"tenant", func(i *requestIdentity) { i.Tenant = "other" }},
		{"permissions", func(i *requestIdentity) { i.Permissions = []string{"admin"} }}, {"claims", func(i *requestIdentity) { i.Claims = map[string]any{"role": "admin"} }},
		{"issuer", func(i *requestIdentity) { i.Issuer = "other" }}, {"expiry", func(i *requestIdentity) { i.Expires = i.Expires.Add(time.Minute) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *identity
			tc.mutate(&copy)
			got, _ := coalescingKey(context.WithValue(context.Background(), identityKey{}, &copy), "p", "api", "production", release, "{ value }", "", map[string]any{"id": 1})
			if got == key {
				t.Fatal("authorization scopes shared")
			}
		})
	}
	for _, scope := range []struct{ project, api, environment string }{{"other", "api", "production"}, {"p", "other", "production"}, {"p", "api", "staging"}} {
		got, _ := coalescingKey(ctx, scope.project, scope.api, scope.environment, release, "{ value }", "", map[string]any{"id": 1})
		if got == key {
			t.Fatal("endpoint scopes shared")
		}
	}
	copy := *release
	copy.Checksum = "new"
	got, _ := coalescingKey(ctx, "p", "api", "production", &copy, "{ value }", "", map[string]any{"id": 1})
	if got == key {
		t.Fatal("release generations shared")
	}
	callerA := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 1, Grants: []sdk.Grant{{Effect: "allow", Permission: "read"}}})
	callerB := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 1, Grants: []sdk.Grant{{Effect: "deny", Permission: "read"}}})
	a, _ := coalescingKey(callerA, "p", "api", "production", release, "{ value }", "", nil)
	b, _ := coalescingKey(callerB, "p", "api", "production", release, "{ value }", "", nil)
	if a == b {
		t.Fatal("platform grant scopes shared")
	}
}
func TestCanonicalDocumentVariablesAndPurity(t *testing.T) {
	schema, issues := validateSDL(`type Query { value(id: ID!): Int } type Mutation { change: Int }`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	first, _ := parseAndValidateQuery(schema, `query Read($id: ID! = 42) { value(id: $id) }`)
	second, _ := parseAndValidateQuery(schema, "# whitespace\nquery Read ($id: ID! = 42) {\n value(id:$id)\n }")
	if canonicalDocument(first) != canonicalDocument(second) {
		t.Fatal("whitespace was not canonicalized")
	}
	op, _ := operationFor(first, "Read")
	a, err := coerceVariables(schema, op, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := coerceVariables(schema, op, map[string]any{"id": "42"})
	if err != nil || runtimeDigest(a) != runtimeDigest(b) {
		t.Fatal("coerced defaults differ", a, b, err)
	}
	prepared := &preparedOperation{op: op, permissionFields: []string{"Query.value"}}
	bindings := &executionBindings{resolvers: map[string]resolverRecord{"Query.value": {SourceID: 1, Operation: "request"}}, sources: map[int64]sourceRecord{1: {Kind: "http", Config: map[string]any{"url": "https://example.test"}}}}
	if pureReadOperation(prepared, bindings, schema) {
		t.Fatal("side effects assumed pure")
	}
	source := bindings.sources[1]
	source.Config["read_only"] = true
	bindings.sources[1] = source
	if !pureReadOperation(prepared, bindings, schema) {
		t.Fatal("explicit pure read rejected")
	}
	mutation, _ := parseAndValidateQuery(schema, `mutation { change }`)
	prepared.op = mutation.Operations[0]
	if pureReadOperation(prepared, bindings, schema) {
		t.Fatal("mutation eligible")
	}
}
func TestRuntimeAdmissionQueueTimeoutFullCancellationAndCleanup(t *testing.T) {
	var r executionRuntime
	l := defaultReleaseLimits()
	l.MaxConcurrentOperations = 1
	l.MaxConcurrentRequests = 2
	l.MaxQueuedOperations = 1
	l.MaxQueueMS = 25
	release, err := r.admit(context.Background(), "api", "operation", l)
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() { _, err := r.admit(context.Background(), "api", "operation", l); waiting <- err }()
	eventuallyRuntime(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.lanes["api:api"].queued == 1 })
	if _, err := r.admit(context.Background(), "api", "operation", l); errorCode(err) != "queue_full" {
		t.Fatal(err)
	}
	if err := <-waiting; errorCode(err) != "queue_timeout" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, err := r.admit(ctx, "api", "operation", l); waiting <- err }()
	eventuallyRuntime(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.lanes["api:api"].queued == 1 })
	cancel()
	if err := <-waiting; errorCode(err) != "execution_cancelled" {
		t.Fatal(err)
	}
	independent, err := r.admit(context.Background(), "api", "other", l)
	if err != nil {
		t.Fatal("per-operation admission was global", err)
	}
	independent()
	release()
	release()
	r.mu.Lock()
	if len(r.lanes) != 0 {
		t.Fatal("idle lanes leak")
	}
	r.mu.Unlock()
}
func TestRuntimeLogFiltersAndSummary(t *testing.T) {
	db := testDB(t)
	for _, raw := range []string{`{"queue_ms":0,"coalesced":false,"backend_reads":2}`, `{"queue_ms":12,"coalesced":true,"backend_reads":1}`, `{}`} {
		if _, err := db.Exec(`INSERT INTO graphql_request_logs(project_id,operation_name,status_code,created_at,runtime_metrics_json) VALUES('p1','Read',200,?,?)`, nowUTC(), raw); err != nil {
			t.Fatal(err)
		}
	}
	f, err := parseLogFiltersQuery(url.Values{"coalesced": {"true"}, "min_queue_ms": {"5"}, "sort_by": {"queue_ms"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := publicLogsFiltered(db, "p1", f)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	metrics := rows[0]["runtime"].(map[string]any)
	if metrics["queue_ms"] != float64(12) {
		t.Fatal(metrics)
	}
	summary, err := logSummary(db, "p1", f, 1000)
	if err != nil || summary["coalesced"] != int64(1) || summary["avg_queue_ms"] != float64(12) {
		t.Fatal(summary, err)
	}
	if _, err := parseLogFiltersArgs(map[string]any{"coalesced": "false"}); err == nil {
		t.Fatal("accepted invalid sharing filter")
	}
}

func TestSharedFailureLocationsUseEachCallersDocument(t *testing.T) {
	schema, issues := validateSDL(`type Query { items: [Row] } type Row { value: Int }`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	doc, issues := parseAndValidateQuery(schema, "query {\n items {\n renamed: value\n }\n}")
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	original := executeResult{Errors: []map[string]any{{"message": "failed", "path": []any{"items", 0, "renamed"}, "locations": []any{map[string]any{"line": 1, "column": 1}}}}}
	own := cloneExecuteResult(original)
	remapErrorLocations(own.Errors, doc.Operations[0].SelectionSet)
	location := own.Errors[0]["locations"].([]any)[0].(map[string]any)
	if location["line"] != 3 || location["column"] != 2 {
		t.Fatal("borrowed error locations", location)
	}
	if original.Errors[0]["locations"].([]any)[0].(map[string]any)["line"] != 1 {
		t.Fatal("shared errors were mutated")
	}
}
