package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	cmd.Env = append(os.Environ(), "APTEVA_APP_PORT="+fmt.Sprint(port), "APTEVA_APP_TOKEN=local-database-test", "APTEVA_PROJECT_ID=", "APTEVA_DATA_DIR="+dir, "DB_PATH="+filepath.Join(dir, "sdk.db"), "APTEVA_GATEWAY_URL=http://127.0.0.1:1")
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
	for _, adapter := range []string{"sqlite", "pebble"} {
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
	if len(v.([]any)) != 2 {
		t.Fatal("app-private database leaked into project list", v)
	}
}
