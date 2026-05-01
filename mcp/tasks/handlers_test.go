package main

// Tier 1 tests — every MCP tool handler exercised against an
// in-memory SQLite. Fast (whole suite <0.05s), runs on every commit.
//
// Three groups in this file:
//   - UNIT      : tool handlers called directly. Asserts handler
//                 logic in isolation.
//   - HTTP      : HTTPRoutes mounted on httptest.Server. Asserts
//                 route wiring + JSON envelopes inside the test
//                 process — no real binary boot.
//   - MANIFEST  : walks MCPTools(), validates schema shape and the
//                 manifest ↔ handler contract.
//
// Tier 2 (real binary, real HTTP/MCP via tk.SpawnSidecar) lives in
// integration_test.go behind //go:build integration.
// Tier 3 (live agent + real LLM) lives in scenarios/*.yaml.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── UNIT ────────────────────────────────────────────────────────

func TestUnit_ToolCreate_RoundTripsThroughDB(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	out, err := app.toolCreate(ctx, map[string]any{
		"instance_id": int64(7),
		"title":       "Ship social v0.2",
		"notes":       "remember to push panel",
	})
	if err != nil {
		t.Fatal(err)
	}
	task := out.(*Task)
	if task.ID == 0 || task.InstanceID != 7 || task.Title != "Ship social v0.2" {
		t.Errorf("task malformed: %+v", task)
	}
	if task.Status != "open" {
		t.Errorf("default status = %q, want open", task.Status)
	}
	// Roundtrip via list.
	got, err := dbList(ctx.AppDB(), 7, "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Ship social v0.2" {
		t.Errorf("list returned %+v", got)
	}
}

func TestUnit_ToolCreate_ValidatesArgs(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	cases := []map[string]any{
		{},                                 // missing both
		{"instance_id": int64(7)},          // missing title
		{"title": "x"},                     // missing instance_id
		{"instance_id": int64(7), "title": ""}, // empty title
	}
	for i, args := range cases {
		if _, err := app.toolCreate(ctx, args); err == nil {
			t.Errorf("case %d: expected error for %+v", i, args)
		}
	}
}

func TestUnit_ToolList_FiltersByStatus(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	for i, status := range []string{"open", "open", "done", "blocked"} {
		dbInsert(ctx.AppDB(), 1, "t"+string(rune('0'+i)), "")
		if status != "open" {
			ctx.AppDB().Exec(`UPDATE tasks SET status=? WHERE id=?`, status, i+1)
		}
	}
	out, _ := app.toolList(ctx, map[string]any{"instance_id": int64(1)})
	if got := len(out.([]Task)); got != 2 {
		t.Errorf("default 'open' filter: got %d, want 2", got)
	}
	out, _ = app.toolList(ctx, map[string]any{"instance_id": int64(1), "status": "all"})
	if got := len(out.([]Task)); got != 4 {
		t.Errorf("status=all: got %d, want 4", got)
	}
	out, _ = app.toolList(ctx, map[string]any{"instance_id": int64(1), "status": "done"})
	if got := len(out.([]Task)); got != 1 {
		t.Errorf("status=done: got %d, want 1", got)
	}
}

func TestUnit_ToolUpdate_PartialFields(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	tk1, _ := dbInsert(ctx.AppDB(), 1, "orig", "old notes")
	if _, err := app.toolUpdate(ctx, map[string]any{
		"task_id": tk1.ID,
		"title":   "renamed",
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbList(ctx.AppDB(), 1, "all")
	if row[0].Title != "renamed" || row[0].Notes != "old notes" {
		t.Errorf("partial update wrong: %+v", row[0])
	}
}

func TestUnit_ToolComplete_FlipsStatus(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	tk1, _ := dbInsert(ctx.AppDB(), 1, "do thing", "")
	if _, err := app.toolComplete(ctx, map[string]any{"task_id": tk1.ID}); err != nil {
		t.Fatal(err)
	}
	row, _ := dbList(ctx.AppDB(), 1, "done")
	if len(row) != 1 || row[0].Status != "done" {
		t.Errorf("complete didn't flip status: %+v", row)
	}
}

// ─── HTTP (in-process) ───────────────────────────────────────────

func TestHTTP_CreateAndList(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()

	body := bytes.NewBufferString(`{"Title":"buy groceries","Notes":"milk, eggs"}`)
	resp, err := http.Post(srv.URL+"/instances/42", "application/json", body)
	must200(t, resp, err)
	var created Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Title != "buy groceries" || created.InstanceID != 42 {
		t.Errorf("created malformed: %+v", created)
	}

	listResp, err := http.Get(srv.URL + "/instances/42")
	must200(t, listResp, err)
	var list []Task
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("list malformed: %+v", list)
	}
}

func TestHTTP_Create_RequiresTitle(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/instances/1", "application/json",
		bytes.NewBufferString(`{"Title":"","Notes":"x"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 on empty title, got %d", resp.StatusCode)
	}
}

func TestHTTP_UpdateAndDelete(t *testing.T) {
	srv := newHTTPServer(t)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/instances/1", "application/json",
		bytes.NewBufferString(`{"Title":"x"}`))
	must200(t, resp, err)
	var t1 Task
	json.NewDecoder(resp.Body).Decode(&t1)

	// Update — server returns 204 on success.
	req, _ := http.NewRequest("PUT", srv.URL+"/tasks/"+itoa(t1.ID),
		bytes.NewBufferString(`{"status":"in_progress"}`))
	upd, _ := http.DefaultClient.Do(req)
	if upd.StatusCode != http.StatusNoContent {
		t.Errorf("update status = %d", upd.StatusCode)
	}

	// Delete.
	delReq, _ := http.NewRequest("DELETE", srv.URL+"/tasks/"+itoa(t1.ID), nil)
	del, _ := http.DefaultClient.Do(delReq)
	if del.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d", del.StatusCode)
	}
	// Confirm gone.
	listResp, err := http.Get(srv.URL + "/instances/1?status=all")
	must200(t, listResp, err)
	body, _ := io.ReadAll(listResp.Body)
	// Empty list serializes as "null\n" or "[]\n" depending on driver;
	// either is fine — what matters is no task with our id appears.
	if strings.Contains(string(body), `"id":`+itoa(t1.ID)) {
		t.Errorf("task still in list after delete: %s", body)
	}
}

// ─── MANIFEST (contract checks) ──────────────────────────────────
//
// Walk MCPTools(), validate schema shape + handler wiring. The real
// JSON-RPC dispatch is exercised in integration_test.go (Tier 2)
// against the actual /mcp endpoint of a running sidecar.

func TestMCP_AllToolsHaveValidShape(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	want := map[string]bool{
		"tasks_create": false, "tasks_list": false,
		"tasks_update": false, "tasks_complete": false,
	}
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
		schema := tool.InputSchema
		if schema["type"] != "object" {
			t.Errorf("%s: schema.type != object", tool.Name)
		}
		if schema["properties"] == nil {
			t.Errorf("%s: schema.properties missing", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool: %q", name)
		}
	}
}

func TestMCP_ToolsCall_CreateThenList(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	create := mcpDispatch(t, app, "tasks_create", map[string]any{
		"instance_id": float64(99), // JSON numbers come in as float64
		"title":       "via MCP",
	}, ctx)
	if create == nil {
		t.Fatal("create returned nil")
	}
	if task, ok := create.(*Task); !ok || task.Title != "via MCP" {
		t.Fatalf("create result = %+v", create)
	}
	list := mcpDispatch(t, app, "tasks_list", map[string]any{
		"instance_id": float64(99),
	}, ctx)
	if got := len(list.([]Task)); got != 1 {
		t.Errorf("list returned %d tasks, want 1", got)
	}
}

func TestMCP_ToolsCall_RequiredArgsEnforced(t *testing.T) {
	ctx := newTasksCtx(t)
	app := &App{}
	// tasks_create with missing required title — handler returns error.
	if _, err := dispatchByName(app, "tasks_create").Handler(ctx, map[string]any{
		"instance_id": float64(1),
	}); err == nil {
		t.Error("expected error for missing title")
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func newTasksCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx
}

// newHTTPServer mounts the app's HTTPRoutes() on httptest.Server the
// same way the SDK's run.go does — straight pattern → handler. The
// app's handlers dispatch by method internally.
func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	newTasksCtx(t) // sets globalCtx
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

// mcpDispatch simulates a JSON-RPC tools/call against the app's MCP
// surface: looks up the tool by name, invokes its handler with the
// (already-typed-from-JSON) args, returns the result. The /mcp
// JSON-RPC endpoint in run.go does the same dispatch — this exercises
// the contract.
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
	for i := range app.MCPTools() {
		t := app.MCPTools()[i]
		if t.Name == name {
			return &t
		}
	}
	return nil
}

func must200(t *testing.T, resp *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("HTTP error: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}

func itoa(n int64) string { return strings.TrimLeft(formatInt(n), "0") }

// formatInt is a tiny zero-alloc int64 → decimal string. The stdlib's
// strconv has the same shape; we inline to keep the test's import set
// minimal.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
