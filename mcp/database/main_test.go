package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
)

func testApp(t *testing.T) *App {
	t.Helper()
	m, e := engine.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	return &App{manager: m}
}
func invoke(a *App, c *sdk.Caller, op string, args map[string]any) (any, error) {
	ctx := context.Background()
	if c != nil {
		ctx = sdk.WithCaller(ctx, c)
	}
	for _, tool := range a.MCPTools() {
		if tool.Name == "db_"+op {
			return tool.HandlerCtx(ctx, nil, args)
		}
	}
	return nil, fmt.Errorf("missing tool")
}
func TestManifestAndTools(t *testing.T) {
	a := testApp(t)
	m := a.Manifest()
	if m.Name != "database" {
		t.Fatal(m.Name)
	}
	seen := map[string]bool{}
	for _, tool := range a.MCPTools() {
		if tool.HandlerCtx == nil {
			t.Fatal("missing caller-aware handler")
		}
		seen[tool.Name] = true
	}
	if len(seen) != len(m.Provides.MCPTools) {
		t.Fatal("manifest/runtime tool mismatch")
	}
	for _, spec := range m.Provides.MCPTools {
		if !seen[spec.Name] {
			t.Fatal(spec.Name)
		}
	}
}

func TestQueryTimeoutContract(t *testing.T) {
	a := testApp(t)
	caller := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
	if _, e := invoke(a, caller, "database_create", map[string]any{"database": "query", "adapter": "pebble"}); e != nil {
		t.Fatal(e)
	}
	if _, e := invoke(a, caller, "collection_create", map[string]any{"database": "query", "collection": "items", "fields": []any{map[string]any{"name": "n", "type": "number"}}}); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"find", "count", "aggregate"} {
		args := map[string]any{"database": "query", "collection": "items", "timeoutMs": 60000}
		if op == "aggregate" {
			args["metrics"] = []any{map[string]any{"name": "n", "op": "count"}}
		}
		if _, e := invoke(a, caller, op, args); e != nil {
			t.Fatal(op, e)
		}
		args["timeoutMs"] = 60001
		if _, e := invoke(a, caller, op, args); e == nil {
			t.Fatal("out-of-range timeout accepted", op)
		}
	}
	if _, e := invoke(a, caller, "insert", map[string]any{"database": "query", "collection": "items", "records": []any{map[string]any{"n": 1}}, "timeoutMs": 60000}); e == nil {
		t.Fatal("write accepted query-only option")
	}
}
func TestScopeAndPermissions(t *testing.T) {
	a := testApp(t)
	allowed := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
	for _, c := range []*sdk.Caller{nil, {AgentID: 1}, {ProjectID: "p", SubjectID: "user"}} {
		if _, e := invoke(a, c, "databases_list", nil); e == nil {
			t.Fatal("missing or delegated identity accepted")
		}
	}
	if _, e := invoke(a, allowed, "database_create", map[string]any{"database": "sales", "project_id": "q"}); e == nil {
		t.Fatal("project override accepted")
	}
	if _, e := invoke(a, allowed, "database_create", map[string]any{"database": "sales"}); e != nil {
		t.Fatal(e)
	}
	denied := &sdk.Caller{ProjectID: "p", AgentID: 2, DefaultEffect: "deny"}
	if _, e := invoke(a, denied, "database_describe", map[string]any{"database": "sales"}); e == nil {
		t.Fatal("denied read succeeded")
	}
	v, e := invoke(a, denied, "databases_list", nil)
	if e != nil || len(v.([]engine.DatabaseInfo)) != 0 {
		t.Fatal("denied database leaked", v, e)
	}
	for _, caller := range []*sdk.Caller{{ProjectID: "q", AgentID: 1}, {ProjectID: "p", AppInstallID: 3, AppName: "crm"}, {ProjectID: "p", AppInstallID: 4, AppName: "tables"}} {
		v, e := invoke(a, caller, "databases_list", nil)
		if e != nil || len(v.([]engine.DatabaseInfo)) != 0 {
			t.Fatal("scope leaked", v, e)
		}
		if _, e := invoke(a, caller, "database_create", map[string]any{"database": "sales", "adapter": "pebble"}); e != nil {
			t.Fatal(e)
		}
	}
	// Unknown nested operations cannot bypass the write-only batch boundary.
	_, e = invoke(a, allowed, "batch", map[string]any{"database": "sales", "operations": []any{map[string]any{"op": "collection_drop", "args": map[string]any{"collection": "items", "confirm": true}}}})
	if e == nil {
		t.Fatal("schema operation accepted in batch")
	}
}

// GraphQL-style sibling calls arrive with bound-caller identity. They keep a
// private namespace by default and reach a project database only by explicit
// scope plus an operator grant, never managing it themselves.
func TestAppProjectDatabaseAccess(t *testing.T) {
	a := testApp(t)
	operator := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
	graphql := &sdk.Caller{ProjectID: "p", AppInstallID: 7, AppName: "graphql", DefaultEffect: "allow"}
	other := &sdk.Caller{ProjectID: "p", AppInstallID: 8, AppName: "crm", DefaultEffect: "allow"}
	elsewhere := &sdk.Caller{ProjectID: "q", AppInstallID: 7, AppName: "graphql", DefaultEffect: "allow"}
	must := func(v any, e error) any {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	denied := func(c *sdk.Caller, op string, args map[string]any) {
		t.Helper()
		var known *engine.Error
		if _, e := invoke(a, c, op, args); !errors.As(e, &known) || known.Code != "permission_denied" {
			t.Fatalf("%s by %s accepted or wrong error: %v", op, c.AppName, e)
		}
	}
	must(invoke(a, operator, "database_create", map[string]any{"database": "prod"}))
	must(invoke(a, operator, "collection_create", map[string]any{"database": "prod", "collection": "orders", "fields": []any{map[string]any{"name": "total", "type": "number"}}}))
	must(invoke(a, operator, "insert", map[string]any{"database": "prod", "collection": "orders", "records": []any{map[string]any{"id": "o1", "total": 5}}}))
	find := map[string]any{"scope": "project", "database": "prod", "collection": "orders"}
	insert := map[string]any{"scope": "project", "database": "prod", "collection": "orders", "records": []any{map[string]any{"id": "o2", "total": 7}}}

	// The default remains the private namespace: prod does not exist there.
	if _, e := invoke(a, graphql, "find", map[string]any{"database": "prod", "collection": "orders"}); e == nil {
		t.Fatal("private namespace resolved the project database")
	}
	denied(graphql, "find", find)
	if v := must(invoke(a, graphql, "databases_list", map[string]any{"scope": "project"})); len(v.([]engine.DatabaseInfo)) != 0 {
		t.Fatal("ungranted project database listed", v)
	}
	denied(graphql, "access_grant", map[string]any{"database": "prod", "app": "graphql", "access": "write"})
	if _, e := invoke(a, operator, "find", map[string]any{"scope": "app", "database": "prod", "collection": "orders"}); e == nil {
		t.Fatal("agent selected an app namespace")
	}
	if _, e := invoke(a, operator, "access_grant", map[string]any{"database": "missing", "app": "graphql", "access": "read"}); e == nil {
		t.Fatal("grant created or named a missing database")
	}

	must(invoke(a, operator, "access_grant", map[string]any{"database": "prod", "app": "graphql", "access": "read"}))
	got := must(invoke(a, graphql, "find", find)).(map[string]any)
	if records := got["records"].([]engine.Record); len(records) != 1 || records[0]["id"] != "o1" {
		t.Fatal("granted read did not reach project data", got)
	}
	if v := must(invoke(a, graphql, "databases_list", map[string]any{"scope": "project"})); len(v.([]engine.DatabaseInfo)) != 1 {
		t.Fatal("granted database not listed", v)
	}
	denied(graphql, "insert", insert)
	denied(graphql, "collection_drop", map[string]any{"scope": "project", "database": "prod", "collection": "orders", "confirm": true})
	denied(graphql, "realtime_configure", map[string]any{"scope": "project", "database": "prod", "enabled": true})
	denied(other, "find", find)
	denied(elsewhere, "find", find)

	must(invoke(a, operator, "access_grant", map[string]any{"database": "prod", "app": "graphql", "access": "write"}))
	must(invoke(a, graphql, "insert", insert))
	if c := must(invoke(a, operator, "count", map[string]any{"database": "prod", "collection": "orders"})).(map[string]any); c["count"] != "2" {
		t.Fatal("app write did not land in project database", c)
	}
	denied(graphql, "index_create", map[string]any{"scope": "project", "database": "prod", "collection": "orders", "index": map[string]any{"name": "by_total", "fields": []any{map[string]any{"field": "total"}}}})
	list := must(invoke(a, operator, "access_list", map[string]any{"database": "prod"})).([]engine.AppAccess)
	if len(list) != 1 || list[0].App != "graphql" || list[0].Access != "write" {
		t.Fatal("access list", list)
	}

	must(invoke(a, operator, "access_revoke", map[string]any{"database": "prod", "app": "graphql"}))
	denied(graphql, "find", find)
	// Dropping and recreating a database must not revive earlier grants.
	must(invoke(a, operator, "access_grant", map[string]any{"database": "prod", "app": "graphql", "access": "read"}))
	must(invoke(a, operator, "database_drop", map[string]any{"database": "prod", "confirm": true}))
	must(invoke(a, operator, "database_create", map[string]any{"database": "prod"}))
	if v := must(invoke(a, graphql, "databases_list", map[string]any{"scope": "project"})); len(v.([]engine.DatabaseInfo)) != 0 {
		t.Fatal("dropped database grant revived", v)
	}
	denied(graphql, "database_describe", map[string]any{"scope": "project", "database": "prod"})
}
func TestHTTPGuardrails(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		body    string
		headers map[string]string
		want    int
	}{
		{`{}`, nil, 403},
		{`{"project_id":"q"}`, map[string]string{"X-Apteva-Project-ID": "p"}, 400},
		{`{} {}`, map[string]string{"X-Apteva-Project-ID": "p"}, 400},
		{`{}`, map[string]string{"X-Apteva-Project-ID": "p", "X-Apteva-Caller-Agent": "7"}, 403},
		{`{}`, map[string]string{"X-Apteva-Project-ID": "p"}, 200},
	} {
		r := httptest.NewRequest("POST", "/operations/databases_list", strings.NewReader(tc.body))
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		a.handleHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: want %d, got %d %s", tc.body, tc.want, w.Code, w.Body.String())
		}
	}
}

func TestSidecar(t *testing.T) {
	if testing.Short() {
		t.Skip("binary integration test")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "database-bin")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	logFile, e := os.Create(filepath.Join(dir, "sidecar.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer logFile.Close()
	cmd := exec.Command(binary)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "APTEVA_APP_PORT="+fmt.Sprint(port), "APTEVA_APP_TOKEN=local-database-test", "APTEVA_PROJECT_ID=", "APTEVA_DATA_DIR="+dir, "DB_PATH="+filepath.Join(dir, "sdk.db"), "APTEVA_GATEWAY_URL=http://127.0.0.1:1", "DATABASE_APTEVADB_SYNC_MODE=full", `APTEVA_APP_CONFIG={"aptevadb_sync_mode":"os"}`)
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	ready := false
	for i := 0; i < 100; i++ {
		resp, e := client.Get(base + "/health")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		b, _ := os.ReadFile(logFile.Name())
		t.Fatalf("sidecar failed to start: %s", b)
	}
	request := func(path string, body any, auth bool, extra map[string]string) (int, any) {
		t.Helper()
		b, _ := json.Marshal(body)
		r, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Apteva-Project-ID", "integration-project")
		if auth {
			r.Header.Set("Authorization", "Bearer local-database-test")
		}
		for k, v := range extra {
			r.Header.Set(k, v)
		}
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var v any
		json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}
	if code, _ := request("/operations/databases_list", map[string]any{}, false, nil); code != 401 {
		t.Fatalf("unauthenticated request returned %d", code)
	}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		for _, step := range []struct {
			op   string
			args map[string]any
		}{
			{"database_create", map[string]any{"database": adapter, "adapter": adapter}},
			{"collection_create", map[string]any{"database": adapter, "collection": "sales", "fields": []any{map[string]any{"name": "amount", "type": "number"}}}},
			{"insert", map[string]any{"database": adapter, "collection": "sales", "records": []any{map[string]any{"amount": 7}, map[string]any{"amount": 11}}}},
			{"aggregate", map[string]any{"database": adapter, "collection": "sales", "metrics": []any{map[string]any{"name": "revenue", "op": "sum", "field": "amount"}}}},
		} {
			code, v := request("/operations/"+step.op, step.args, true, nil)
			if code != 200 {
				t.Fatalf("%s/%s: %d %v", adapter, step.op, code, v)
			}
			if step.op == "aggregate" {
				row := v.(map[string]any)["rows"].([]any)[0].(map[string]any)
				if row["revenue"] != float64(18) {
					t.Fatal(row)
				}
			}
		}
		if adapter == "aptevadb" {
			code, value := request("/operations/explain", map[string]any{"database": adapter, "collection": "sales"}, true, nil)
			if code != 200 || value.(map[string]any)["syncMode"] != "os" || value.(map[string]any)["synchronousWrites"] != true {
				t.Fatal("mounted app config must override environment and keep writes synchronous", code, value)
			}
		}
	}

	// Exercise SSE through the actual SDK server and token gate, not just a handler.
	if code, _ := request("/realtime/watch", map[string]any{"database": "aptevadb", "collections": []string{"sales"}}, false, nil); code != 401 {
		t.Fatal("watch token gate", code)
	}
	if code, v := request("/operations/realtime_configure", map[string]any{"database": "aptevadb", "enabled": true}, true, nil); code != 200 {
		t.Fatal("enable capture", code, v)
	}
	streamCtx, stopStream := context.WithTimeout(context.Background(), 5*time.Second)
	streamReq, _ := http.NewRequestWithContext(streamCtx, "POST", base+"/realtime/watch", strings.NewReader(`{"database":"aptevadb","collections":["sales"],"initial":{"op":"find","args":{"collection":"sales"}}}`))
	streamReq.Header.Set("Authorization", "Bearer local-database-test")
	streamReq.Header.Set("X-Apteva-Project-ID", "integration-project")
	stream, e := http.DefaultClient.Do(streamReq)
	if e != nil {
		stopStream()
		t.Fatal(e)
	}
	if stream.StatusCode != 200 || stream.Header.Get("Content-Type") != "text/event-stream" {
		stream.Body.Close()
		stopStream()
		t.Fatal("SDK streaming", stream.Status)
	}
	scanner := bufio.NewScanner(stream.Body)
	readEvent := func(want string) {
		t.Helper()
		for scanner.Scan() {
			if scanner.Text() == "event: "+want {
				return
			}
		}
		t.Fatal("SDK event missing", want, scanner.Err())
	}
	readEvent("snapshot")
	readEvent("ready")
	if code, v := request("/operations/insert", map[string]any{"database": "aptevadb", "collection": "sales", "records": []any{map[string]any{"amount": 1}}}, true, nil); code != 200 {
		t.Fatal(code, v)
	}
	readEvent("invalidate")
	stream.Body.Close()
	stopStream()
	for attempt := 0; attempt < 100; attempt++ {
		code, v := request("/operations/realtime_describe", map[string]any{"database": "aptevadb"}, true, nil)
		if code != 200 {
			t.Fatal(code, v)
		}
		if v.(map[string]any)["stats"].(map[string]any)["Watchers"] == float64(0) {
			break
		}
		if attempt == 99 {
			t.Fatal("SDK disconnect leaked watcher", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, v := request("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "db_database_create", "arguments": map[string]any{"database": "private"}}}, true, map[string]string{sdk.HeaderBoundCallerInstallID: "42", sdk.HeaderBoundCallerAppName: "crm"})
	if code != 200 {
		t.Fatalf("MCP returned %d: %v", code, v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), `"isError":true`) || strings.Contains(string(b), `"error":`) {
		t.Fatalf("MCP failed: %s", b)
	}
	_, v = request("/operations/databases_list", map[string]any{}, true, nil)
	if len(v.([]any)) != 3 {
		t.Fatal("app-private database leaked into project list", v)
	}
	// Through the real bridge headers, a granted app reaches the project
	// database the dashboard owns; an ungranted app gets a tool error.
	projectCount := func(app string) string {
		_, v := request("/mcp", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "db_count", "arguments": map[string]any{"scope": "project", "database": "sqlite", "collection": "sales"}}}, true, map[string]string{sdk.HeaderBoundCallerInstallID: "42", sdk.HeaderBoundCallerAppName: app})
		b, _ := json.Marshal(v)
		return string(b)
	}
	if out := projectCount("crm"); !strings.Contains(out, "no access to project database sqlite") {
		t.Fatal("ungranted app reached project database", out)
	}
	if code, v := request("/operations/access_grant", map[string]any{"database": "sqlite", "app": "crm", "access": "read"}, true, nil); code != 200 {
		t.Fatal(code, v)
	}
	if out := projectCount("crm"); strings.Contains(out, `"isError":true`) || !strings.Contains(out, `\"count\":\"2\"`) {
		t.Fatal("granted app could not read project database", out)
	}
	if out := projectCount("tables"); !strings.Contains(out, "no access to project database sqlite") {
		t.Fatal("grant leaked to another app", out)
	}
}
