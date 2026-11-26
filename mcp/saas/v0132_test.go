package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// stubToolSchemas replaces the tools/list lookup for one test and counts
// lookups per app.
func stubToolSchemas(t *testing.T, byApp map[string]appToolSchemas, failApps map[string]bool) map[string]int {
	t.Helper()
	calls := map[string]int{}
	prev := lookupAppToolSchemas
	resetActionSchemaCache()
	lookupAppToolSchemas = func(pid, appName string) (appToolSchemas, error) {
		calls[appName]++
		if failApps[appName] {
			return nil, errors.New("tools/list unavailable")
		}
		return byApp[appName], nil
	}
	t.Cleanup(func() {
		lookupAppToolSchemas = prev
		resetActionSchemaCache()
	})
	return calls
}

func strictRunSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}}}
}

func keyedSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}}}
}

func activateWithAction(t *testing.T, appName, tool string) (*platformStub, *sdk.AppCtx, func()) {
	t.Helper()
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "workspace", "name": "Workspace"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "workspace", "event": "account_active", "app_name": appName, "tool_name": tool,
		"args": map[string]any{"name": "ws-{{account.slug}}", "image": "ghcr.io/apteva/go:latest"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "go@example.com", "slug": "go-acme", "plan_key": "workspace"}); err != nil {
		t.Fatalf("account create: %v", err)
	}
	return pf, ctx, func() { db.Close() }
}

func TestV0132_ActionWithoutDeclaredKeyGetsNoIdempotencyKey(t *testing.T) {
	lookups := stubToolSchemas(t, map[string]appToolSchemas{"containers": {"containers_run": strictRunSchema()}}, nil)
	pf, _, done := activateWithAction(t, "containers", "containers_run")
	defer done()
	call := findCall(pf.calls, "containers", "containers_run")
	if call == nil {
		t.Fatalf("containers_run not called: %+v", pf.calls)
	}
	if _, ok := call.Input["idempotency_key"]; ok {
		t.Fatalf("containers_run got idempotency_key though its schema does not declare it: %v", call.Input)
	}
	if call.Input["name"] != "ws-go-acme" || call.Input["_project_id"] != "proj-test" {
		t.Fatalf("other args changed: %v", call.Input)
	}
	if lookups["containers"] != 1 {
		t.Fatalf("lookups=%d, want 1", lookups["containers"])
	}
}

func TestV0132_ActionWithDeclaredKeyGetsIdempotencyKey(t *testing.T) {
	stubToolSchemas(t, map[string]appToolSchemas{"hosting": {"hosting_tenant_create": keyedSchema()}}, nil)
	pf, _, done := activateWithAction(t, "hosting", "hosting_tenant_create")
	defer done()
	call := findCall(pf.calls, "hosting", "hosting_tenant_create")
	if call == nil || strFromAny(call.Input["idempotency_key"]) == "" {
		t.Fatalf("declared idempotency_key was not sent: %+v", call)
	}
}

func TestV0132_UnreadableSchemaLeavesKeyOut(t *testing.T) {
	lookups := stubToolSchemas(t, nil, map[string]bool{"containers": true})
	pf, _, done := activateWithAction(t, "containers", "containers_run")
	defer done()
	call := findCall(pf.calls, "containers", "containers_run")
	if call == nil {
		t.Fatal("action must still run when the schema cannot be read")
	}
	if _, ok := call.Input["idempotency_key"]; ok {
		t.Fatal("key sent although the schema could not be read")
	}
	// The failure is cached briefly, so a burst of actions does not
	// re-query the app for every call.
	if actionAcceptsIdempotencyKey("proj-test", "containers", "containers_run") || lookups["containers"] != 1 {
		t.Fatalf("failure not cached: lookups=%d", lookups["containers"])
	}
}

func TestV0132_AppActionInputKeepsStoredArgs(t *testing.T) {
	stubToolSchemas(t, map[string]appToolSchemas{"containers": {"containers_run": strictRunSchema()}}, nil)
	args := map[string]any{"name": "x", "idempotency_key": "k"}
	input := appActionInput("proj-test", PlanAction{AppName: "containers", ToolName: "containers_run"}, args)
	if _, ok := input["idempotency_key"]; ok {
		t.Fatal("key not removed from the call input")
	}
	if args["idempotency_key"] != "k" {
		t.Fatal("stored args were modified")
	}
}

func TestV0132_FetchToolSchemasThroughAppProxy(t *testing.T) {
	var gotPath, gotAuth, gotInstall, gotProject, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotProject = r.URL.Query().Get("project_id")
		gotAuth = r.Header.Get("Authorization")
		gotInstall = r.Header.Get("X-Apteva-App-Install-ID")
		var rpc map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &rpc)
		gotMethod, _ = rpc["method"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{
			map[string]any{"name": "containers_run", "inputSchema": strictRunSchema()},
			map[string]any{"name": "containers_keyed", "inputSchema": keyedSchema()},
		}}})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL+"/")
	t.Setenv("APTEVA_APP_TOKEN", "tok-saas")
	t.Setenv("APTEVA_INSTALL_ID", "144")

	tools, err := fetchAppToolSchemas("proj-test", "containers")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/apps/callback/apps/containers/proxy/mcp" || gotProject != "proj-test" || gotMethod != "tools/list" {
		t.Fatalf("path=%s project=%s method=%s", gotPath, gotProject, gotMethod)
	}
	if gotAuth != "Bearer tok-saas" || gotInstall != "144" {
		t.Fatalf("auth=%q install=%q", gotAuth, gotInstall)
	}
	if schemaDeclaresProperty(tools["containers_run"], "idempotency_key") || !schemaDeclaresProperty(tools["containers_keyed"], "idempotency_key") {
		t.Fatalf("parsed schemas wrong: %v", tools)
	}
}

func TestV0132_FetchToolSchemasErrors(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "")
	if _, err := fetchAppToolSchemas("proj-test", "containers"); err == nil {
		t.Fatal("missing gateway env must fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "target app is not bound: containers", http.StatusForbidden)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "tok")
	if _, err := fetchAppToolSchemas("proj-test", "containers"); err == nil {
		t.Fatal("non-2xx must fail")
	}
	if _, err := parseToolsList([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`)); err == nil {
		t.Fatal("JSON-RPC error must fail")
	}
}
