package main

// Tier 1 tests for the status app — handlers + manifest contract,
// in-process (no real sidecar binary). Tier 2 (real sidecar via
// tk.SpawnSidecar) is in integration_test.go behind //go:build
// integration. Tier 3 (live agent + LLM) is scenarios/*.yaml.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── UNIT ────────────────────────────────────────────────────────

func TestUnit_ToolSet_DefaultsToInfoTone(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	out, err := app.toolSet(ctx, map[string]any{
		"instance_id": int64(7),
		"message":     "running migrations",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(*Status)
	if s.Tone != "info" {
		t.Errorf("default tone = %q, want info", s.Tone)
	}
	if s.Message != "running migrations" {
		t.Errorf("message round-trip wrong: %+v", s)
	}
}

func TestUnit_ToolSet_RejectsInvalidTone(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	_, err := app.toolSet(ctx, map[string]any{
		"instance_id": int64(1),
		"message":     "x",
		"tone":        "panic", // not in the validTones map
	})
	if err == nil {
		t.Error("expected error for invalid tone")
	}
}

func TestUnit_ToolSet_UpsertSemantics(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	app.toolSet(ctx, map[string]any{"instance_id": int64(1), "message": "first"})
	app.toolSet(ctx, map[string]any{"instance_id": int64(1), "message": "second", "tone": "working"})
	// Single row per instance — second call overwrites first.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM status_status WHERE instance_id=1`).Scan(&n)
	if n != 1 {
		t.Errorf("expected upsert to keep one row, got %d", n)
	}
	out, _ := app.toolGet(ctx, map[string]any{"instance_id": int64(1)})
	s := out.(*Status)
	if s.Message != "second" || s.Tone != "working" {
		t.Errorf("upsert didn't update fields: %+v", s)
	}
}

func TestUnit_ToolGet_AbsentReturnsEmpty(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	out, err := app.toolGet(ctx, map[string]any{"instance_id": int64(99)})
	if err != nil {
		t.Fatal(err)
	}
	// No row → returns {instance_id, message:""} placeholder, NOT nil.
	res := out.(map[string]any)
	if res["message"] != "" {
		t.Errorf("expected empty message for missing row, got %+v", res)
	}
}

func TestUnit_ToolClear_RemovesRow(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	app.toolSet(ctx, map[string]any{"instance_id": int64(5), "message": "x"})
	if _, err := app.toolClear(ctx, map[string]any{"instance_id": int64(5)}); err != nil {
		t.Fatal(err)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM status_status WHERE instance_id=5`).Scan(&n)
	if n != 0 {
		t.Errorf("clear didn't remove row, %d remaining", n)
	}
}

func TestUnit_ToolSet_ValidatesArgs(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	cases := []map[string]any{
		{},
		{"instance_id": int64(1)},        // missing message
		{"message": "x"},                  // missing instance_id
		{"instance_id": int64(1), "message": ""},
	}
	for i, args := range cases {
		if _, err := app.toolSet(ctx, args); err == nil {
			t.Errorf("case %d: expected error for %+v", i, args)
		}
	}
}

// ─── HTTP (in-process) ───────────────────────────────────────────

func TestHTTP_GetMissingReturns204(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/instances/1234")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 204 for missing status, got %d (%s)", resp.StatusCode, body)
	}
}

func TestHTTP_GetAfterMCPSet(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	// Set via the MCP tool path, read via HTTP — proves both paths
	// share the same store.
	app := &App{}
	if _, err := app.toolSet(globalCtx, map[string]any{
		"instance_id": int64(42),
		"message":     "hello",
		"tone":        "success",
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/instances/42")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Message != "hello" || s.Tone != "success" {
		t.Errorf("HTTP read mismatch: %+v", s)
	}
}

func TestHTTP_DeleteClears(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	app := &App{}
	app.toolSet(globalCtx, map[string]any{"instance_id": int64(7), "message": "x"})
	req, _ := http.NewRequest("DELETE", srv.URL+"/instances/7", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	// Confirm.
	var n int
	globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM status_status WHERE instance_id=7`).Scan(&n)
	if n != 0 {
		t.Errorf("delete didn't remove row, %d left", n)
	}
}

// ─── MANIFEST (contract checks) ──────────────────────────────────

func TestMCP_AllToolsHaveValidShape(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	want := map[string]bool{"status_set": false, "status_get": false, "status_clear": false}
	for _, tool := range tools {
		if _, expected := want[tool.Name]; !expected {
			t.Errorf("unexpected tool: %q", tool.Name)
			continue
		}
		want[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s: description empty", tool.Name)
		}
		if tool.Handler == nil {
			t.Errorf("%s: handler nil", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: schema.type != object", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool: %q", name)
		}
	}
}

func TestMCP_ToolsCall_SetGetClear(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	// status_set
	if r := mcpDispatch(t, app, "status_set", map[string]any{
		"instance_id": float64(1),
		"message":     "via mcp",
		"tone":        "working",
	}, ctx); r == nil {
		t.Fatal("set returned nil")
	}
	// status_get
	r := mcpDispatch(t, app, "status_get", map[string]any{
		"instance_id": float64(1),
	}, ctx)
	s, ok := r.(*Status)
	if !ok {
		t.Fatalf("get returned %T (%+v), want *Status", r, r)
	}
	if s.Message != "via mcp" || s.Tone != "working" {
		t.Errorf("get mismatch: %+v", s)
	}
	// status_clear
	if _, err := dispatchByName(app, "status_clear").Handler(ctx, map[string]any{
		"instance_id": float64(1),
	}); err != nil {
		t.Fatal(err)
	}
	// Confirm cleared.
	r = mcpDispatch(t, app, "status_get", map[string]any{
		"instance_id": float64(1),
	}, ctx)
	res := r.(map[string]any) // get returns a placeholder map when row absent
	if res["message"] != "" {
		t.Errorf("expected empty after clear, got %+v", res)
	}
}

func TestMCP_ToolsCall_RequiredArgsEnforced(t *testing.T) {
	ctx := newStatusCtx(t)
	app := &App{}
	if _, err := dispatchByName(app, "status_set").Handler(ctx, map[string]any{
		"instance_id": float64(1),
		// missing message
	}); err == nil {
		t.Error("expected error for missing message")
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func newStatusCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx
}

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	newStatusCtx(t)
	app := &App{}
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		method, pattern, handler := r.Method, r.Pattern, r.Handler
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if method != "" && req.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handler(w, req)
		})
	}
	return httptest.NewServer(mux)
}

func mcpDispatch(t *testing.T, app *App, name string, args map[string]any, ctx *sdk.AppCtx) any {
	t.Helper()
	tool := dispatchByName(app, name)
	if tool == nil {
		t.Fatalf("no MCP tool named %q", name)
	}
	out, err := tool.Handler(ctx, args)
	if err != nil {
		t.Fatalf("tool %q: %v", name, err)
	}
	return out
}

func dispatchByName(app *App, name string) *sdk.Tool {
	tools := app.MCPTools()
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
